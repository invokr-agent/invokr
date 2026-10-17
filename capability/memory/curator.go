package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/summary"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"

	"go.uber.org/zap"
)

// curatorQueueSize bounds the backlog of pending summarization batches. It is
// small because summarization only triggers every Nth turn, so a few in-flight
// batches is already generous.
const curatorQueueSize = 8

// summaryJob is a unit of background memory work: a raw-event batch awaiting
// summarization, or — when title is set — a session awaiting its label.
type summaryJob struct {
	sessionID string
	limit     int
	title     bool
}

// curator owns the event-driven memory write path: appending raw turns and —
// every Nth turn — summarizing them into durable records. Pinned markdown
// (USER.md/MEMORY.md) is not written here; it changes only through the
// explicit memory tool. Event dispatch itself stays synchronous and ordered; only the
// summarizer round trip can be pushed onto a background worker via
// startWorker, per review-remediation.md §2.3.
type curator struct {
	// manager is a snapshot of the owning TieredManager. Its fields are
	// pointers/interfaces, so the snapshot stays valid for the curator's
	// lifetime even though TieredManager itself is a value type.
	manager TieredManager

	// jobs is nil until startWorker runs; HandleEvent falls back to a
	// synchronous summarization call in that case (used by tests and any
	// manager built as a bare struct literal without a runner).
	jobs      chan summaryJob
	wg        sync.WaitGroup
	closeOnce sync.Once

	// titled records sessions already queued for titling this run, so the job
	// is enqueued once rather than after every turn. Guarded by titledMu.
	titledMu sync.Mutex
	titled   map[string]struct{}
}

// titler is the optional titling capability, satisfied by chatSummarizer. It is
// reached by type assertion rather than added to memorySummarizer, because the
// zerfoo worker satisfies that interface and cannot generate titles usably.
type titler interface {
	Title(ctx context.Context, lines []string) (string, error)
}

// titleTimeout bounds a titling round trip. It is generous because the call
// runs on the background worker, not on the turn loop or the exit path.
const titleTimeout = 90 * time.Second

// titleTranscriptMessages caps how many transcript messages feed the title.
const titleTranscriptMessages = 6

// newCurator builds a curator bound to m. The background worker is not
// started; call startWorker to enable async summarization.
func newCurator(m TieredManager) *curator { return &curator{manager: m} }

// startWorker launches the single goroutine that runs memory summarization
// off the caller's goroutine. Summarization can involve a slow summarizer
// round trip, so running it inline on the event dispatcher's goroutine would
// block the turn loop.
func (c *curator) startWorker() {
	c.jobs = make(chan summaryJob, curatorQueueSize)
	c.wg.Add(1)
	go c.run()
}

func (c *curator) run() {
	defer c.wg.Done()
	for job := range c.jobs {
		// Detached context: the turn's ctx may be canceled once the turn
		// ends, but this background work must still complete. Errors are
		// best-effort — the next threshold crossing re-triggers summarization.
		if err := c.runJob(job); err != nil {
			zap.L().Debug("background memory job failed", zap.Error(err))
		}
	}
}

// enqueue submits a job. When the queue is full it drops the job rather than
// blocking the turn loop: summarization is best-effort and the next Nth-turn
// crossing will re-trigger it.
func (c *curator) enqueue(job summaryJob) {
	select {
	case c.jobs <- job:
	default:
		zap.L().Debug("memory summary queue full; dropping batch", zap.String("session", job.sessionID))
	}
}

// Close stops the worker and waits for in-flight work to finish. Closing the
// channel lets the worker drain any already-queued batches before it exits.
// Close is idempotent so both TieredManager.Close and tests draining the
// queue early can call it safely. It is a no-op when the worker was never
// started.
func (c *curator) Close() error {
	if c.jobs == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		close(c.jobs)
	})
	c.wg.Wait()
	return nil
}

// runJob dispatches one unit of background work.
func (c *curator) runJob(job summaryJob) error {
	// Detached context: the turn's ctx may be canceled once the turn ends, but
	// this background work must still complete.
	if !job.title {
		return c.summarizeRecentEvents(context.Background(), job.sessionID, job.limit)
	}
	gen, ok := c.manager.summarizer.(titler)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	defer cancel()
	return c.titleSession(ctx, c.manager.sessions, gen, job.sessionID)
}

// queueTitle enqueues a one-off titling job the first time a session is seen
// without a label. Titling runs in the background rather than at shutdown: a
// reasoning model spends hundreds of tokens before emitting the title, which
// would stall every exit.
func (c *curator) queueTitle(sessionID string) {
	store := c.manager.sessions
	if c.manager.noTitles || store == nil || c.jobs == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	c.titledMu.Lock()
	if c.titled == nil {
		c.titled = make(map[string]struct{})
	}
	_, already := c.titled[sessionID]
	c.titled[sessionID] = struct{}{}
	c.titledMu.Unlock()
	if already || store.Title(sessionID) != "" {
		return
	}
	c.enqueue(summaryJob{sessionID: sessionID, title: true})
}

// titleSession generates and stores the label for one session.
func (c *curator) titleSession(ctx context.Context, store *sessionstore.Store, gen titler, id string) error {
	messages, err := store.Load(ctx, id)
	if err != nil {
		return err
	}
	if len(messages) > titleTranscriptMessages {
		messages = messages[:titleTranscriptMessages]
	}
	lines := make([]string, 0, len(messages))
	for _, msg := range messages {
		if text := strings.TrimSpace(msg.Text); text != "" {
			lines = append(lines, msg.Role+": "+text)
		}
	}
	title, err := gen.Title(ctx, lines)
	if err != nil {
		return err
	}
	return store.SetTitle(id, title)
}

// HandleEvent is the event-driven write path: append the raw turn and — every
// Nth turn — trigger summarization (async if a worker is running, synchronous
// otherwise). Pinned markdown (USER.md/MEMORY.md) is written only through the
// explicit memory tool; keyword auto-curation was removed (issue #317 B10).
func (c *curator) HandleEvent(ctx context.Context, ev events.Event) error {
	m := c.manager
	if ev.Type != events.EventAfterResponse || ev.Response == nil {
		return nil
	}
	if ev.ParentSessionID != "" {
		// Subagent turns are not auto-curated into persistent memory; the
		// delegate_task tool records their input/output explicitly instead.
		return nil
	}
	// Record raw turns unconditionally — they're the substrate for both the
	// pre-threshold recall fallback and later summarization. Only summarization
	// needs the worker.
	if err := c.appendResponseEvents(ctx, ev); err != nil {
		return err
	}
	// Only after the transcript is on disk: the title job reads it back, and
	// the worker picks the job up immediately.
	c.queueTitle(ev.SessionID)
	if c.summarizerOrWorker() == nil {
		return nil
	}
	count, err := m.sqlite.CountMemoryEvents(ctx, ev.SessionID)
	if err != nil {
		return err
	}
	threshold := m.zerfoo.MaxRawEventsBeforeSummary
	if threshold <= 0 {
		threshold = 12
	}
	if count == 0 || count%threshold != 0 {
		return nil
	}
	if c.jobs != nil {
		c.enqueue(summaryJob{sessionID: ev.SessionID, limit: threshold})
		return nil
	}
	return c.summarizeRecentEvents(ctx, ev.SessionID, threshold)
}

// summarizerOrWorker returns the active summarization role: the dedicated
// chat-endpoint summarizer if wired, else the (zerfoo) worker, else nil. Routing
// summaries to the chat model lets summarization run even when zerfoo is off.
func (c *curator) summarizerOrWorker() memorySummarizer {
	if c.manager.summarizer != nil {
		return c.manager.summarizer
	}
	if c.manager.worker != nil {
		return c.manager.worker
	}
	return nil
}

func (c *curator) appendResponseEvents(ctx context.Context, ev events.Event) error {
	m := c.manager
	var metadata map[string]string
	if len(ev.Response.ToolOutcomes) > 0 {
		data, err := json.Marshal(ev.Response.ToolOutcomes)
		if err != nil {
			return fmt.Errorf("encode tool outcomes: %w", err)
		}
		metadata = map[string]string{"tool_outcomes": string(data)}
	}
	turns := []struct {
		role, content string
		metadata      map[string]string
		tools         sessionstore.ToolRecord
	}{
		{role: "user", content: ev.Response.Query},
		{role: "assistant", content: ev.Response.Text, metadata: metadata,
			tools: sessionstore.ToolRecord{Calls: ev.Response.ToolOutcomes, Count: ev.Response.ToolCallCount, Evidence: ev.Response.ExecutionEvidence}},
	}
	for _, t := range turns {
		if strings.TrimSpace(t.content) == "" {
			continue
		}
		if _, err := m.sqlite.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: ev.SessionID, Role: t.role, Content: t.content, Metadata: t.metadata, CreatedAt: ev.Timestamp}); err != nil {
			return err
		}
		// Also mirror the turn into the JSONL session transcript so every UI
		// (TUI, plain, exec) is searchable via session_search — the exec CLI used
		// to be the only writer.
		if m.sessions != nil && strings.TrimSpace(ev.SessionID) != "" {
			if _, err := m.sessions.AppendTurn(ctx, ev.SessionID, t.role, t.content, t.tools); err != nil {
				return err
			}
		}
	}
	return nil
}

// summarizeRecentEvents summarizes the batch and then runs the forgetting decay
// sweep. The sweep piggybacks on this existing checkpoint (every Nth turn) so
// there is no separate scheduler; it is a no-op when forgetting is disabled.
func (c *curator) summarizeRecentEvents(ctx context.Context, sessionID string, limit int) error {
	if err := c.curateSummary(ctx, sessionID, limit); err != nil {
		return err
	}
	c.manager.sweep(ctx)
	return nil
}

func (c *curator) curateSummary(ctx context.Context, sessionID string, limit int) error {
	m := c.manager
	recentEvents, err := m.sqlite.RecentMemoryEvents(ctx, sessionID, limit)
	if err != nil {
		return err
	}
	if len(recentEvents) == 0 {
		return nil
	}
	input := summary.StructuredSummaryInput{Events: make([]summary.MemoryEvent, 0, len(recentEvents))}
	for _, ev := range recentEvents {
		input.Events = append(input.Events, summary.MemoryEvent{
			SessionID: ev.SessionID, Role: ev.Role, Content: ev.Content, Metadata: ev.Metadata, CreatedAt: ev.CreatedAt,
		})
	}
	result, err := c.summarizerOrWorker().Summarize(ctx, input)
	if err != nil {
		return err
	}
	if !result.ShouldStore || result.Importance < m.zerfoo.MinImportanceToStore {
		return nil
	}
	if _, err := m.Save(ctx, core.Record{
		Scope: config.ScopeProject, Kind: core.KindSummary, Source: core.SourceRuntime,
		Content: result.Summary, Summary: result.Summary, SessionID: sessionID}); err != nil {
		return err
	}
	for _, fact := range result.Facts {
		if fact.Confidence < 0.5 {
			continue
		}
		content := strings.TrimSpace(strings.Join([]string{fact.Subject, fact.Predicate, fact.Object}, " "))
		if content == "" {
			continue
		}
		if _, err := m.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact,
			Source: core.SourceRuntime, Content: content, SessionID: sessionID}); err != nil {
			return err
		}
	}
	return nil
}
