package agent

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/invokr-ai/invokr/capability/agentprompt"
	"github.com/invokr-ai/invokr/support/strutil"

	inference "github.com/rivt-ai/go-inference-router"
)

const (
	// minRetainedMessages is a floor on the retained tail, not the retention
	// rule: planRetention may widen it to keep tool calls paired with results.
	minRetainedMessages      = 4
	conversationSummaryLabel = agentprompt.SummaryLabel
	elidedToolOutput         = "[output elided to fit context]"
	// noReductionNotice is the elision pass reporting that it changed nothing.
	noReductionNotice = "context could not be reduced"
	elidedNotice      = "elided old tool output"
	// elidedToolOutputFormat says how much went, so the model can tell an
	// elided result from a tool that genuinely returned nothing.
	elidedToolOutputFormat = "[%d KB of output elided to fit context]"
	summaryFilesPrefix     = "\nFile operations: "
)

type contextWindowState struct {
	warningSent                                 bool
	providerPromptTokens, providerMessageTokens int64
	tokenEstimateScale                          float64
}

type summaryFuture struct {
	key, summary string
	end          int // one past the last summarized message (#513)
	done         chan struct{}
	err          error
}

// wait blocks until the prefetch finishes or ctx ends, and returns whichever
// error that produced.
func (f *summaryFuture) wait(ctx context.Context) error {
	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s contextWindowState) afterRewrite(messages []inference.Message, window int64, warningPercent int) contextWindowState {
	s.providerPromptTokens, s.providerMessageTokens = 0, 0
	// The calibrated estimate, which is what contextTokens reduces to now the
	// provider counts above are zeroed - the basis the trigger in Prepare
	// measures in. The raw estimate reads low enough to clear the flag for a
	// context still over the threshold: one wasted prefetch per cycle (#531).
	if window > 0 && calibratedMessageTokens(messages, s.tokenEstimateScale)*100/window < int64(warningPercent) {
		s.warningSent = false
	}
	return s
}

func contextTokens(messages []inference.Message, state contextWindowState) int64 {
	estimate := calibratedMessageTokens(messages, state.tokenEstimateScale)
	if state.providerPromptTokens <= 0 {
		return estimate
	}
	return state.providerPromptTokens + max(0, estimate-state.providerMessageTokens)
}

func calibratedMessageTokens(messages []inference.Message, scale float64) int64 {
	if scale <= 0 {
		scale = 1
	}
	return max(1, int64(float64(estimatedMessageTokens(messages))*scale))
}

func summaryKey(messages []inference.Message) string {
	return strutil.HashHex(agentprompt.SummaryTranscript(messages))
}

func summarySegment(messages []inference.Message, plan retentionPlan) []inference.Message {
	return messages[plan.prefixEnd:plan.tailStart]
}

func estimatedMessageTokens(messages []inference.Message) int64 {
	var total int64
	for _, message := range messages {
		total += estimatedMessageTokenCount(message)
	}
	return total
}

func estimatedMessageTokenCount(message inference.Message) int64 {
	bytes := len(message.Content)
	for _, call := range message.ToolCalls {
		bytes += len(call.Name) + len(call.Arguments)
	}
	// ponytail: len/4 token estimate; real tokenizer if drift bites.
	return int64(bytes+3) / 4
}

// elisionBudget restates the compaction threshold in the basis elidePass
// measures in: the trigger uses provider-calibrated contextTokens (system prompt
// and tool schemas included), elidePass raw estimates over the messages alone.
func elisionBudget(messages []inference.Message, state contextWindowState, windowSize int64, compactPercent int) int64 {
	scale := cmp.Or(state.tokenEstimateScale, 1)
	overhead := contextTokens(messages, state) - calibratedMessageTokens(messages, scale)
	return int64(float64(windowSize*int64(compactPercent)/100-overhead) / scale)
}

// elideOldToolOutput returns the rewritten transcript and a notice that reports
// what actually happened — never a reduction that did not occur.
func elideOldToolOutput(messages []inference.Message, plan retentionPlan, budget int64) ([]inference.Message, string) {
	out := append([]inference.Message(nil), messages...)
	before := estimatedMessageTokens(out)
	protected := pinnedWithOwners(out, plan.pinned)
	tokens := elideRange(out, plan.prefixEnd, plan.tailStart, budget, before, false, protected)
	tokens = elideRange(out, plan.prefixEnd, plan.tailStart, budget, tokens, true, protected)
	// The middle can be too small to carry the overage on its own — one
	// enormous tool result inside the retained tail is exactly the shape that
	// pushed these runs past 100%. Reducing something is always better than
	// reporting failure and continuing over the window, so keep going into
	// the tail, oldest first, stopping short of the last few turns.
	tokens = elideRange(out, plan.tailStart, len(out)-2, budget, tokens, false, nil)
	tokens = elideNewestPass(out, plan, budget, tokens)
	if tokens == before {
		return out, noReductionNotice
	}
	return out, elidedNotice
}

// elideNewestPass cuts the newest tool results down to a head slice once
// everything older has already given way. One huge result the model just asked
// for is the shape that leaves a request over the window with nothing left to
// elide; a truncated head still answers the call, an over-window request does
// not.
func elideNewestPass(out []inference.Message, plan retentionPlan, budget, tokens int64) int64 {
	for index := max(plan.tailStart, len(out)-2); index >= 0 && index < len(out) && tokens > budget; index++ {
		message := out[index]
		if message.Role != inference.RoleTool {
			continue
		}
		count := estimatedMessageTokenCount(message)
		keep := int(max(0, (budget-(tokens-count))*4*8/10))
		if keep >= len(message.Content) {
			continue
		}
		replacement := inference.ToolMessage(message.ToolCallID,
			message.Content[:keep]+"\n"+fmt.Sprintf(elidedToolOutputFormat, (len(message.Content)-keep)/1024))
		out[index] = replacement
		tokens += estimatedMessageTokenCount(replacement) - count
	}
	return tokens
}

// elideRange rewrites messages in [start, stop) until the estimate fits the
// budget: tool results, or tool-call arguments when blankArguments is set.
// Protected indices (pinned results and their owners) are left verbatim; the
// tail pass passes none, since there it protects only the newest exchange by
// stopping short of it.
func elideRange(out []inference.Message, start, stop int, budget, tokens int64, blankArguments bool, protected map[int]bool) int64 {
	for index := start; index < stop && tokens > budget; index++ {
		if protected[index] {
			continue
		}
		message := out[index]
		replacement, ok := elidedMessage(message, blankArguments)
		if !ok {
			continue
		}
		out[index] = replacement
		tokens += estimatedMessageTokenCount(replacement) - estimatedMessageTokenCount(message)
	}
	return tokens
}

// elisionStub names the size of what was dropped; below a kilobyte the size
// adds nothing, and the bare marker keeps the stub shorter than the content.
func elisionStub(message inference.Message) string {
	if kb := len(message.Content) / 1024; kb > 0 {
		return fmt.Sprintf(elidedToolOutputFormat, kb)
	}
	return elidedToolOutput
}

func elidedMessage(message inference.Message, blankArguments bool) (inference.Message, bool) {
	if blankArguments {
		if message.Role != inference.RoleAssistant || len(message.ToolCalls) == 0 {
			return message, false
		}
		calls := append([]inference.ToolCall(nil), message.ToolCalls...)
		for callIndex := range calls {
			calls[callIndex].Arguments = `{"_elided":"tool call arguments removed to fit context"}`
		}
		return inference.AssistantMessage(message.Content, calls...), true
	}
	switch {
	case message.Role == inference.RoleTool:
		return inference.ToolMessage(message.ToolCallID, elisionStub(message)), true
	case message.Role == inference.RoleUser && strings.Contains(message.Content, `"type":"tool_result"`):
		return inference.UserMessage(fmt.Sprintf(`{"type":"tool_result","content":%q}`, elisionStub(message))), true
	}
	return message, false
}
