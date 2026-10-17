package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// contextBudget owns the context-window compaction state: the configured window
// size, the force-compaction flag, and the prefetched conversation summary. It
// keeps that concern testable in isolation from the wider Runner.
type contextBudget struct {
	windowSize                     int64
	providerContext                *ProviderContext
	backgroundContext              context.Context
	warningPercent, compactPercent int
	gateway                        ModelGateway
	events                         events.Dispatcher
	logger                         *zap.Logger
	parentSessionID                string
	forceCompaction                atomic.Bool
	summaryMu                      sync.Mutex
	summaryFuture                  *summaryFuture
	// writesWorkspace reports whether a tool mutates the workspace; nil-safe
	// (pinning is skipped when unset, e.g. in tests).
	writesWorkspace func(string) bool
	// todo reads authoritative task state only after a context rewrite.
	todo          func() string
	toolState     func() string
	spill         spillStore
	workspaceRoot string
}

// plan computes the retention boundary for the current transcript: a tail
// budget of ContextTailPercent of the window (zero window keeps the flat
// floor), the calibrated estimator scale, and workspace-write pinning. On a
// forced compaction the budgeted tail may swallow a transcript far smaller
// than the window; the explicit request still deserves a summary, so fall
// back to the flat floor.
func (b *contextBudget) plan(messages []inference.Message, scale float64, forced bool) retentionPlan {
	plan := planRetention(messages, b.contextWindowTokens()*constants.ContextTailPercent/100, scale, b.writesWorkspace)
	if forced && !plan.summarizable() {
		plan = planRetention(messages, 0, 1, b.writesWorkspace)
	}
	return plan
}

// RequestCompaction forces compaction on the next Prepare regardless of the
// current token threshold.
func (b *contextBudget) RequestCompaction() {
	if b == nil {
		return
	}
	b.forceCompaction.Store(true)
}

// Prepare compacts the transcript past the context threshold.
func (b *contextBudget) Prepare(ctx context.Context, sessionID string, messages []inference.Message, state contextWindowState) ([]inference.Message, contextWindowState) {
	if b == nil || len(messages) == 0 {
		return messages, state
	}
	windowSize := b.contextWindowTokens()
	if windowSize <= 0 {
		return messages, state
	}
	forced := b.forceCompaction.Swap(false)
	tokens := contextTokens(messages, state)
	percent := tokens * 100 / windowSize
	if !state.warningSent && percent >= int64(b.warningThreshold()) {
		events.Notice(ctx, b.events, sessionID, b.parentSessionID, fmt.Sprintf("context window at %d%% — compaction will trigger soon", percent))
		state.warningSent = true
		// Reserve the summary's own completion: the prompt alone fitting still
		// overflowed a 64k unified KV at the 95% warning and evicted the main
		// conversation's prefix cache (#577).
		b.prefetchSummary(messages, state.tokenEstimateScale, windowSize-tokens-int64(summarizerMaxTokens(windowSize)))
	}
	if !forced && percent < int64(b.compactThreshold()) {
		return messages, state
	}
	plan := b.plan(messages, state.tokenEstimateScale, forced)
	if forced && percent < int64(b.compactThreshold()) && !plan.summarizable() {
		b.forceCompaction.Store(true)
		return messages, state
	}
	return b.compact(ctx, sessionID, messages, plan, state, windowSize, percent)
}

// compact reduces the transcript and reports what happened. A transcript that
// is already over the window is elided first: a summarizer call made over the
// window is rejected, which is how a single huge tool result used to end in
// "context could not be reduced".
func (b *contextBudget) compact(ctx context.Context, sessionID string, messages []inference.Message, plan retentionPlan, state contextWindowState, windowSize, percent int64) ([]inference.Message, contextWindowState) {
	events.Notice(ctx, b.events, sessionID, b.parentSessionID, "context compaction in progress")
	target := elisionBudget(messages, state, windowSize, max(1, min(b.warningThreshold(), b.compactThreshold())-15))
	var elided bool
	// A prefetched summary for this segment needs no summarizer call, so the
	// over-window transcript can go straight to it; eliding first would change
	// the segment and throw the prefetched work away.
	if percent >= 100 && !b.summaryPrefetched(ctx, messages, plan) {
		messages, plan, elided = b.elideFirst(ctx, sessionID, messages, plan, state, windowSize)
		// Elision alone can leave nothing to summarize; that is a completed
		// compaction, not a summarizer failure.
		if elided && !plan.summarizable() {
			events.Notice(ctx, b.events, sessionID, b.parentSessionID, "context compaction complete")
			return b.afterCompaction(messages, state, windowSize, true, target)
		}
	}
	compacted, err := b.summarizeMessages(ctx, messages, plan)
	if err == nil {
		events.Notice(ctx, b.events, sessionID, b.parentSessionID, "context compaction complete")
		return b.afterCompaction(compacted, state, windowSize, true, target)
	}
	b.logger.Warn("context summarization failed; eliding old tool output", zap.Error(err))
	before := estimatedMessageTokens(messages)
	if b.spill != nil {
		messages = b.spillHistory(messages, true)
		plan.pinned = pinnedResultIndices(messages, plan.prefixEnd, plan.tailStart, b.writesWorkspace)
	}
	elided = elided || estimatedMessageTokens(messages) < before
	compacted, notice := elideOldToolOutput(messages, plan, elisionBudget(messages, state, windowSize, b.compactThreshold()))
	// A second pass with nothing left to elide is not a failure to reduce when
	// the first pass already shrank the transcript.
	if elided && notice == noReductionNotice {
		notice = elidedNotice
	}
	events.Notice(ctx, b.events, sessionID, b.parentSessionID, "context summarization failed; "+notice)
	return b.afterCompaction(compacted, state, windowSize, notice != noReductionNotice, target)
}

// afterCompaction appends a retained snapshot after the surviving conversation.
// Ordinary turns keep their prefix unchanged; failed, no-op compactions add nothing.
func (b *contextBudget) afterCompaction(messages []inference.Message, state contextWindowState, windowSize int64, changed bool, target int64) ([]inference.Message, contextWindowState) {
	if changed && b.workspaceRoot != "" {
		messages = append(messages, inference.UserMessage(fmt.Sprintf("Runtime workspace root and default tool cwd: %q. Relative file/output paths resolve here. A shell cd only affects that command; do not prepend the workspace directory name again.", b.workspaceRoot)))
	}
	if changed && b.todo != nil {
		if items := b.todo(); items != "" {
			messages = append(messages, inference.UserMessage("Current todo list (from the todo tool; supersedes earlier snapshots):\n"+items))
		}
	}
	if changed && b.toolState != nil {
		if snapshot := b.toolState(); snapshot != "" {
			messages = append(messages, inference.UserMessage(snapshot))
		}
	}
	if changed && estimatedMessageTokens(messages) > target {
		messages = b.spillHistory(messages, false)
		messages, _ = elideOldToolOutput(messages, b.plan(messages, state.tokenEstimateScale, true), target)
	}
	return messages, state.afterRewrite(messages, windowSize, b.warningThreshold())
}

// elideFirst runs the deterministic elision pass before the summarizer, for a
// transcript that is already over the window. It reports whether anything
// changed and re-plans retention over the reduced transcript.
func (b *contextBudget) elideFirst(ctx context.Context, sessionID string, messages []inference.Message, plan retentionPlan, state contextWindowState, windowSize int64) ([]inference.Message, retentionPlan, bool) {
	archived := b.spillHistory(messages, true)
	plan.pinned = pinnedResultIndices(archived, plan.prefixEnd, plan.tailStart, b.writesWorkspace)
	reduced, notice := elideOldToolOutput(archived, plan, elisionBudget(messages, state, windowSize, b.compactThreshold()))
	if notice == noReductionNotice && estimatedMessageTokens(reduced) >= estimatedMessageTokens(messages) {
		return messages, plan, false
	}
	events.Notice(ctx, b.events, sessionID, b.parentSessionID, elidedNotice)
	return reduced, b.plan(reduced, state.tokenEstimateScale, false), true
}

func (b *contextBudget) warningThreshold() int {
	return cmp.Or(b.warningPercent, constants.ContextWarningPercent)
}

// compactThreshold is the configured percent, or else the window less an
// absolute reserve for one response: a fixed 80% compacted a 61k window at 49k,
// ten times in one M1 run, where pi ran to the physical 65k and compacted once
// (#577, #600). An overflow still forces a compaction and a retry.
func (b *contextBudget) compactThreshold() int {
	if b.compactPercent != 0 {
		return b.compactPercent
	}
	return constants.ContextCompactThreshold(b.contextWindowTokens())
}

func (b *contextBudget) contextWindowTokens() int64 {
	if b == nil {
		return 0
	}
	return cmp.Or(b.providerContext.ContextWindowTokens(), b.windowSize)
}

// summaryPrefetched reports whether a background summary usable by this plan
// exists. It waits for an in-flight prefetch; a failed one counts as absent.
func (b *contextBudget) summaryPrefetched(ctx context.Context, messages []inference.Message, plan retentionPlan) bool {
	trimmed, future := b.prefetched(messages, plan)
	return future != nil && coversEnough(messages, trimmed, plan) && future.wait(ctx) == nil
}

// prefetchSummary starts the summary for the next compaction in the background.
// headroom is what is left of the server window once the current prompt is
// counted: with a unified KV cache the prefetch and the agent turn share one
// pool, so a summary that does not fit alongside the turn overflows the server
// and kills both requests (#499). In that case there is no prefetch and
// compaction runs synchronously at the threshold instead.
//
// The caller reserves the summary's completion; the agent turn's completion
// and the summary preamble are still unreserved slop.
func (b *contextBudget) prefetchSummary(messages []inference.Message, scale float64, headroom int64) {
	summarizer, ok := b.gateway.(conversationSummarizer)
	if !ok {
		return
	}
	plan := b.plan(messages, scale, false)
	if calibratedMessageTokens(summarySegment(messages, plan), scale) > headroom {
		// At the warning threshold the default segment never fits, so
		// summarize only its oldest ≈headroom now; compaction keeps the rest
		// verbatim (#513).
		plan = chunkRetention(messages, headroom, scale, b.writesWorkspace)
	}
	segment := summarySegment(messages, plan)
	if len(segment) == 0 {
		return
	}
	if segmentTokens := calibratedMessageTokens(segment, scale); segmentTokens > headroom {
		b.logger.Info("skipping the summary prefetch: it would not fit alongside the current turn", zap.Int64("segment_tokens", segmentTokens), zap.Int64("headroom_tokens", headroom))
		return
	}
	key := summaryKey(segment)
	b.summaryMu.Lock()
	if b.summaryFuture != nil && b.summaryFuture.key == key {
		b.summaryMu.Unlock()
		return
	}
	future := &summaryFuture{key: key, end: plan.tailStart, done: make(chan struct{})}
	b.summaryFuture = future
	b.summaryMu.Unlock()
	// Copy: segment shares a backing array with the caller's message slice,
	// which may be appended to (and reallocated in place) while this goroutine
	// is still reading it.
	segmentCopy := append([]inference.Message(nil), segment...)
	prefetchCtx, cancel := context.WithTimeout(cmp.Or[context.Context](b.backgroundContext, context.Background()), 2*time.Minute)
	go func() {
		defer cancel()
		defer close(future.done)
		future.summary, future.err = summarizer.SummarizeConversation(prefetchCtx, segmentCopy)
	}()
}

// prefetched returns the plan trimmed to what the current prefetch already
// summarized, or the plan unchanged and a nil future when none is usable.
func (b *contextBudget) prefetched(messages []inference.Message, plan retentionPlan) (retentionPlan, *summaryFuture) {
	b.summaryMu.Lock()
	defer b.summaryMu.Unlock()
	return plan.trimTo(messages, b.summaryFuture, b.writesWorkspace)
}

func (b *contextBudget) summarizeMessages(ctx context.Context, messages []inference.Message, plan retentionPlan) ([]inference.Message, error) {
	if !plan.summarizable() {
		return nil, fmt.Errorf("not enough history to summarize")
	}
	summarizer, ok := b.gateway.(conversationSummarizer)
	if !ok {
		return nil, fmt.Errorf("model gateway does not support conversation summaries")
	}
	summary, plan, err := b.resolveSummary(ctx, summarizer, messages, plan)
	if err != nil {
		return nil, fmt.Errorf("summarize conversation: %w", err)
	}
	if strings.TrimSpace(summary) == "" {
		return nil, fmt.Errorf("summarize conversation: empty response")
	}
	files := summaryFiles(summarySegment(messages, plan), b.writesWorkspace)
	// Resolve against the original transcript first: changing archived payloads
	// before matching the key discards a valid prefetch and repeats its model call.
	if b.spill != nil {
		messages = b.spillHistory(messages, true)
		plan.pinned = pinnedResultIndices(messages, plan.prefixEnd, plan.tailStart, b.writesWorkspace)
	}
	compacted := append([]inference.Message{}, messages[:plan.prefixEnd]...)
	// A generated summary is assistant-derived data, never a new user instruction.
	// Keep it outside the system role for templates with one leading system message.
	compacted = append(compacted, inference.AssistantMessage(conversationSummaryLabel+fmt.Sprintf("%q", strings.TrimSpace(summary))+files))
	// "Never drop the last edit": pinned write results sit in the summarized
	// middle, so carry them over verbatim (ADR 0013).
	compacted = append(compacted, pinnedMessages(messages, plan)...)
	compacted = append(compacted, messages[plan.tailStart:]...)
	// Checked post-condition: losing a compaction is recoverable, shipping a
	// request the provider rejects is not. Only orphans compaction introduced
	// count — a transcript that arrived orphaned is not this code's doing.
	if orphans := len(orphanedToolCallIDs(compacted)); orphans > len(orphanedToolCallIDs(messages)) {
		return nil, fmt.Errorf("compaction orphaned %d tool call ids", orphans)
	}
	return compacted, nil
}

// resolveSummary takes a usable prefetched summary, with the plan trimmed to
// its segment; when there is none, or it failed, it summarizes the plan's whole
// middle now.
func (b *contextBudget) resolveSummary(ctx context.Context, summarizer conversationSummarizer, messages []inference.Message, plan retentionPlan) (string, retentionPlan, error) {
	if trimmed, future := b.prefetched(messages, plan); future != nil && coversEnough(messages, trimmed, plan) && future.wait(ctx) == nil {
		return future.summary, trimmed, nil
	}
	summary, err := summarizer.SummarizeConversation(ctx, summarySegment(messages, plan))
	return summary, plan, err
}

// minPrefetchCoveragePercent is how much of the plan's summarizable middle a
// prefetched prefix must cover for compaction to stop there. A prefetch
// chunked to the headroom left at the trigger (#513) can cover a tenth of the
// middle: taking it as the whole compaction frees almost nothing, the next
// turn is over the trigger again, and the shrinking headroom makes each chunk
// smaller still - 14 compactions in 6 minutes on marathon M1 (#529). Below
// this, the middle is summarized in full now instead.
const minPrefetchCoveragePercent = 50

// coversEnough reports whether the prefetched prefix trimmed out of plan is a
// big enough share of plan's middle to be worth compacting to on its own.
func coversEnough(messages []inference.Message, trimmed, plan retentionPlan) bool {
	full := estimatedMessageTokens(summarySegment(messages, plan))
	return full <= 0 || estimatedMessageTokens(summarySegment(messages, trimmed))*100/full >= minPrefetchCoveragePercent
}

// summaryFiles keeps path metadata across compactions; file contents remain in
// the summary/tail policy. JSON quoting keeps filenames from becoming prose.
func summaryFiles(segment []inference.Message, writes func(string) bool) string {
	files := map[string][]string{"read": {}, "modified": {}}
	for _, message := range segment {
		for kind, paths := range carriedFiles(message) {
			files[kind] = append(files[kind], paths...)
		}
		for _, call := range message.ToolCalls {
			files["read"] = append(files["read"], readPaths(call)...)
			if writes != nil && writes(call.Name) {
				var args struct{ Path string }
				_ = json.Unmarshal([]byte(call.Arguments), &args)
				if args.Path != "" {
					files["modified"] = append(files["modified"], args.Path)
				}
			}
		}
	}
	for kind, paths := range files {
		slices.Sort(paths)
		files[kind] = slices.Compact(paths)
	}
	files["read"] = slices.DeleteFunc(files["read"], func(path string) bool {
		return slices.Contains(files["modified"], path)
	})
	if len(files["read"])+len(files["modified"]) == 0 {
		return ""
	}
	data, _ := json.Marshal(files)
	return summaryFilesPrefix + string(data)
}

func carriedFiles(message inference.Message) map[string][]string {
	if message.Role != inference.RoleAssistant || !strings.HasPrefix(message.Content, conversationSummaryLabel) {
		return nil
	}
	_, data, ok := strings.Cut(message.Content, summaryFilesPrefix)
	var files map[string][]string
	if !ok || json.Unmarshal([]byte(data), &files) != nil {
		return nil
	}
	return map[string][]string{"read": files["read"], "modified": files["modified"]}
}
