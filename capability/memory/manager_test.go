package memory

import (
	"context"
	"errors"
	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/capability/memory/markdown"
	"github.com/invokr-agent/invokr/capability/memory/sqlite"
	"github.com/invokr-agent/invokr/capability/memory/summary"
	"github.com/invokr-agent/invokr/capability/sessionstore"
	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
)

func newTestSQLiteStore(t *testing.T) *sqlite.Memory {
	t.Helper()
	store, err := sqlite.NewStore(sqlite.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func newTestMarkdownMemory(t *testing.T, dir string) *markdown.Memory {
	t.Helper()
	memory, err := markdown.NewMemory(markdown.Config{Dir: dir})
	if err != nil {
		t.Fatalf("NewMemory() error = %v", err)
	}
	return memory
}

type mockDispatcher struct {
	events []events.Event
}

func (d *mockDispatcher) Dispatch(_ context.Context, event events.Event) {
	d.events = append(d.events, event)
}

func TestNewManagerFromConfigUsesConfiguredMemoryDir(t *testing.T) {
	root := t.TempDir()
	memoryDir := filepath.Join(root, "nested")

	_, err := NewManagerFromConfigWithEvents(config.AppConfig{
		Memory: config.MemoryConfig{
			Dir: memoryDir,
		},
	}, &mockDispatcher{})
	if err != nil {
		t.Fatalf("NewManagerFromConfig() error = %v", err)
	}

	if _, err := os.Stat(memoryDir); err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat memory dir: %v", err)
	}
}

func TestNewManagerFromConfigWithEventsRequiresDispatcher(t *testing.T) {
	_, err := NewManagerFromConfigWithEvents(config.AppConfig{
		Memory: config.MemoryConfig{
			Dir: t.TempDir(),
		},
	}, nil)
	if err == nil {
		t.Fatal("NewManagerFromConfigWithEvents() error = nil, want error")
	}
}

type fakeEmbedder struct {
	vectors [][]float32
}

func (e fakeEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return e.vectors, nil
}

func (e fakeEmbedder) EmbedQueries(context.Context, []string) ([][]float32, error) {
	return e.vectors, nil
}

type fakeMemoryWorker struct {
	vectors      [][]float32
	summary      summary.MemorySummary
	embedCalls   int
	summaryCalls int
}

func (w *fakeMemoryWorker) Close() error { return nil }

func (w *fakeMemoryWorker) Summarize(context.Context, summary.StructuredSummaryInput) (summary.MemorySummary, error) {
	w.summaryCalls++
	return w.summary, nil
}

func (w *fakeMemoryWorker) Embed(context.Context, []string) ([][]float32, error) {
	w.embedCalls++
	return w.vectors, nil
}

func (w *fakeMemoryWorker) EmbedQueries(context.Context, []string) ([][]float32, error) {
	w.embedCalls++
	return w.vectors, nil
}

// failingEmbedWorker always fails Embed, simulating the embed step erroring
// out after a SQLite write already landed.
type failingEmbedWorker struct{}

func (failingEmbedWorker) Close() error { return nil }
func (failingEmbedWorker) Summarize(context.Context, summary.StructuredSummaryInput) (summary.MemorySummary, error) {
	return summary.MemorySummary{}, nil
}
func (failingEmbedWorker) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embed unavailable")
}
func (failingEmbedWorker) EmbedQueries(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embed unavailable")
}

func TestVectorSourceEmbedsQueryAndSearchesStore(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	rec, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Vector recall memory.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.SaveVector(ctx, rec.ID, []float32{1, 0}); err != nil {
		t.Fatalf("SaveVector() error = %v", err)
	}

	source := vectorSource{embedder: fakeEmbedder{vectors: [][]float32{{1, 0}}}, store: store}
	hits, err := source.Recall(ctx, core.Query{Query: "recall memory", Limit: 1})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != rec.ID {
		t.Fatalf("Recall() = %#v, want vector hit %q", hits, rec.ID)
	}
}

func TestManagerSaveAndUpdateVectorizesRuntimeRecords(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	worker := &fakeMemoryWorker{vectors: [][]float32{{1, 0}}}
	manager := TieredManager{
		markdown: nil,
		sqlite:   store,
		worker:   worker,
		events:   &mockDispatcher{},
	}

	saved, err := manager.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindSummary,
		Source:  core.SourceRuntime,
		Content: "Semantic vector memory.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	hits, err := store.RecallVector(ctx, []float32{1, 0}, core.Query{Limit: 1})
	if err != nil {
		t.Fatalf("RecallVector() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != saved.ID {
		t.Fatalf("Save vector hits = %#v, want %q", hits, saved.ID)
	}

	worker.vectors = [][]float32{{0, 1}}
	if _, err := manager.Update(ctx, saved.ID, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindSummary,
		Source:  core.SourceRuntime,
		Content: "Updated semantic vector memory.",
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	hits, err = store.RecallVector(ctx, []float32{0, 1}, core.Query{Limit: 1})
	if err != nil {
		t.Fatalf("RecallVector(updated) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != saved.ID {
		t.Fatalf("updated vector hits = %#v, want %q", hits, saved.ID)
	}
	if worker.embedCalls != 2 {
		t.Fatalf("embed calls = %d, want 2", worker.embedCalls)
	}
}

func TestHandleEventSummarizesThresholdAndStoresVectors(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	memoryDir := t.TempDir()
	worker := &fakeMemoryWorker{
		vectors: [][]float32{{1, 0}},
		summary: summary.MemorySummary{
			Summary:     "Project chose strict Tier 2 vector failures.",
			Facts:       []summary.MemoryFact{{Subject: "project", Predicate: "chose", Object: "strict vector failures", Confidence: 0.9}},
			Importance:  0.8,
			ShouldStore: true,
		},
	}
	manager := TieredManager{
		markdown: newTestMarkdownMemory(t, memoryDir),
		sqlite:   store,
		worker:   worker,
		events:   &mockDispatcher{},
		zerfoo: config.ZerfooMemoryConfig{
			MaxRawEventsBeforeSummary: 2,
			MinImportanceToStore:      0.35,
		},
	}

	err := manager.HandleEvent(ctx, events.Event{
		Type:      events.EventAfterResponse,
		SessionID: "s1",
		Response:  &events.ResponsePayload{Query: "remember tier2 policy", Text: "strict vector failures"},
	})
	if err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	if worker.summaryCalls != 1 {
		t.Fatalf("summary calls = %d, want 1", worker.summaryCalls)
	}
	events, err := store.RecentMemoryEvents(ctx, "s1", 2)
	if err != nil {
		t.Fatalf("RecentMemoryEvents() error = %v", err)
	}
	if len(events) != 2 || events[0].Role != "user" || events[1].Role != "assistant" {
		t.Fatalf("events = %#v, want user then assistant", events)
	}
	hits, err := store.RecallVector(ctx, []float32{1, 0}, core.Query{Limit: 5})
	if err != nil {
		t.Fatalf("RecallVector() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("vector hits = %#v, want summary and fact", hits)
	}
}

type fakeChatClient struct {
	calls  int
	prompt string
	models []string
}

func (f *fakeChatClient) Summarize(_ context.Context, in clients.SummaryInput) (string, error) {
	f.calls++
	f.prompt = in.Prompt
	f.models = append(f.models, in.Model)
	return `{"summary":"User prefers Rust.","facts":[],"importance":0.9,"should_store":true}`, nil
}

type mutableModelSource struct{ model string }

func (s *mutableModelSource) Model() string { return s.model }

func TestChatSummarizerFollowsActiveModel(t *testing.T) {
	chat := &fakeChatClient{}
	models := &mutableModelSource{model: "model-a"}
	summarizer := chatSummarizer{client: chat, model: "fallback", models: models}
	input := summary.StructuredSummaryInput{Events: []summary.MemoryEvent{{Content: "remember this"}}}
	if _, err := summarizer.Summarize(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	models.model = "model-b"
	if _, err := summarizer.Summarize(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(chat.models, ","); got != "model-a,model-b" {
		t.Fatalf("summary models = %q, want model-a,model-b", got)
	}
}

// Summarization runs via the chat provider even with no zerfoo worker, and the
// summary lands as a searchable record. Guards issue M's fix.
func TestHandleEventSummarizesViaChatProviderWithoutWorker(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	chat := &fakeChatClient{}
	manager := TieredManager{
		markdown:   newTestMarkdownMemory(t, t.TempDir()),
		sqlite:     store,
		summarizer: chatSummarizer{client: chat},
		events:     &mockDispatcher{},
		zerfoo:     config.ZerfooMemoryConfig{MaxRawEventsBeforeSummary: 2, MinImportanceToStore: 0.35},
	}

	if err := manager.HandleEvent(ctx, events.Event{
		Type:      events.EventAfterResponse,
		SessionID: "s1",
		Response:  &events.ResponsePayload{Query: "I love rust", Text: "noted"},
	}); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	if chat.calls != 1 {
		t.Fatalf("chat summarize calls = %d, want 1", chat.calls)
	}
	recs, err := store.List(ctx, core.Query{Kinds: []core.Kind{core.KindSummary}, Limit: 5})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(recs) != 1 || recs[0].Content != "User prefers Rust." {
		t.Fatalf("summary records = %#v, want one chat-produced summary", recs)
	}
}

// blockingSummarizer parks Summarize until release is closed, signalling
// started when the call begins. It lets tests prove summarization runs off
// the caller's goroutine.
type blockingSummarizer struct {
	started chan struct{}
	release chan struct{}
	result  summary.MemorySummary
}

func (b *blockingSummarizer) Summarize(context.Context, summary.StructuredSummaryInput) (summary.MemorySummary, error) {
	close(b.started)
	<-b.release
	return b.result, nil
}

// Summarization must not run on the event handler's goroutine, and Close must
// wait for in-flight work before returning (plan item 2.3 / finding #7).
func TestHandleEventSummarizesOffTurnLoopAndCloseDrains(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	blocking := &blockingSummarizer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		result:  summary.MemorySummary{Summary: "Async summary.", Importance: 0.9, ShouldStore: true},
	}
	manager := TieredManager{
		markdown:   newTestMarkdownMemory(t, t.TempDir()),
		sqlite:     store,
		summarizer: blocking,
		events:     &mockDispatcher{},
		zerfoo:     config.ZerfooMemoryConfig{MaxRawEventsBeforeSummary: 2, MinImportanceToStore: 0.35},
	}
	manager.curator = newCurator(manager)
	manager.curator.startWorker()

	// HandleEvent must return while Summarize is still parked; a synchronous
	// implementation would deadlock here because release is not yet closed.
	if err := manager.HandleEvent(ctx, events.Event{
		Type:      events.EventAfterResponse,
		SessionID: "s1",
		Response:  &events.ResponsePayload{Query: "remember async", Text: "noted"},
	}); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	select {
	case <-blocking.started:
	case <-time.After(5 * time.Second):
		t.Fatal("summary worker never picked up the enqueued batch")
	}

	close(blocking.release)
	if err := manager.curator.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// Close returned only after the worker finished, so the summary is stored.
	recs, err := store.List(ctx, core.Query{Kinds: []core.Kind{core.KindSummary}, Limit: 5})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(recs) != 1 || recs[0].Content != "Async summary." {
		t.Fatalf("summary records = %#v, want the async-produced summary", recs)
	}
}

func TestRecallFallsBackToRawEventsBeforeSummaryThreshold(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	manager := TieredManager{
		markdown: newTestMarkdownMemory(t, t.TempDir()),
		sqlite:   store,
		events:   &mockDispatcher{},
	}
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "s1", Role: "user", Content: "deploy the parser tomorrow"}); err != nil {
		t.Fatalf("AppendMemoryEvent() error = %v", err)
	}

	hits, err := manager.Recall(ctx, core.Query{Query: "what did i say", SessionID: "s1", Limit: 3})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.Content != "deploy the parser tomorrow" {
		t.Fatalf("hits = %#v, want the raw turn as fallback", hits)
	}
	if hits[0].Score != recentContextScore {
		t.Fatalf("fallback score = %v, want low-confidence %v", hits[0].Score, recentContextScore)
	}
}

// The raw-turn fallback must not answer queries scoped or kind-filtered away
// from session episodes (issue F): a project/fact query should get nothing
// rather than unrelated recent chat.
func TestRecallFallbackSkipsNonEpisodeScopedQueries(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	manager := TieredManager{
		markdown: newTestMarkdownMemory(t, t.TempDir()),
		sqlite:   store,
		events:   &mockDispatcher{},
	}
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "s1", Role: "user", Content: "deploy the parser tomorrow"}); err != nil {
		t.Fatalf("AppendMemoryEvent() error = %v", err)
	}

	for _, q := range []core.Query{
		{Query: "anything", SessionID: "s1", Limit: 3, Kinds: []core.Kind{core.KindFact}},
		{Query: "anything", SessionID: "s1", Limit: 3, Scopes: []config.MemoryScope{config.ScopeProject}},
	} {
		hits, err := manager.Recall(ctx, q)
		if err != nil {
			t.Fatalf("Recall(%#v) error = %v", q, err)
		}
		if len(hits) != 0 {
			t.Fatalf("Recall(%#v) = %#v, want no fallback hits", q, hits)
		}
	}
}

// The shared event handler must mirror each top-level turn into the JSONL
// session transcript so session_search sees TUI/plain sessions, not just exec.
func TestHandleEventWritesSessionTranscript(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sessions := sessionstore.New(dir)
	manager := TieredManager{
		markdown: newTestMarkdownMemory(t, dir),
		sqlite:   newTestSQLiteStore(t),
		sessions: sessions,
		events:   &mockDispatcher{},
	}

	// Empty SessionID must not error — the transcript append is simply skipped.
	if err := manager.HandleEvent(ctx, events.Event{
		Type:     events.EventAfterResponse,
		Response: &events.ResponsePayload{Query: "what's the capital of Finland?", Text: "Helsinki"},
	}); err != nil {
		t.Fatalf("HandleEvent() with empty session error = %v", err)
	}
	if err := manager.HandleEvent(ctx, events.Event{
		Type:      events.EventAfterResponse,
		SessionID: "s1",
		Response:  &events.ResponsePayload{Query: "what's the capital of France?", Steered: []string{"and its population?"}, Text: "Paris"},
	}); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	// Subagent turns (ParentSessionID set) must not pollute the transcript.
	if err := manager.HandleEvent(ctx, events.Event{
		Type:            events.EventAfterResponse,
		SessionID:       "s1",
		ParentSessionID: "parent",
		Response:        &events.ResponsePayload{Query: "subagent q", Text: "subagent a"},
	}); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}

	messages, err := sessions.Load(ctx, "s1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 3 || messages[0].Text != "what's the capital of France?" ||
		messages[1].Role != "user" || messages[1].Text != "and its population?" || messages[2].Text != "Paris" {
		t.Fatalf("transcript = %+v, want the top-level turn with its mid-run message before the answer", messages)
	}
}

func TestHandleEventStoresToolOutcomesWithoutCuratingGuardText(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := newTestSQLiteStore(t)
	markdown := newTestMarkdownMemory(t, dir)
	manager := TieredManager{markdown: markdown, sqlite: store, events: &mockDispatcher{}}

	err := manager.HandleEvent(ctx, events.Event{
		Type:      events.EventAfterResponse,
		SessionID: "s1",
		Response: &events.ResponsePayload{
			Query: "deploy now",
			Text:  "The project deployment succeeded.",
			ToolOutcomes: []turn.ToolOutcome{{
				Tool: "shell", Kind: turn.OutcomeKindError, Detail: "exit 1",
			}},
		},
	})
	if err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	raw, err := store.RecentMemoryEvents(ctx, "s1", 2)
	if err != nil {
		t.Fatalf("RecentMemoryEvents() error = %v", err)
	}
	if len(raw) != 2 || raw[1].Content != "The project deployment succeeded." || !strings.Contains(raw[1].Metadata["tool_outcomes"], `"detail":"exit 1"`) {
		t.Fatalf("raw events = %#v, want clean response with structured failure metadata", raw)
	}
	entries, err := markdown.Project().List(ctx)
	if err != nil {
		t.Fatalf("List project memory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed response was curated into project memory: %#v", entries)
	}
}

// When the current session has no raw turns yet (first message asks to recall a
// prior conversation), the fallback must search across all sessions, not just
// the empty current one.
func TestRecallRawFallsBackAcrossSessionsWhenCurrentEmpty(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	manager := TieredManager{
		markdown: newTestMarkdownMemory(t, t.TempDir()),
		sqlite:   store,
		events:   &mockDispatcher{},
	}
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "prior", Role: "user", Content: "we discussed the deploy plan"}); err != nil {
		t.Fatalf("AppendMemoryEvent() error = %v", err)
	}

	hits, err := manager.Recall(ctx, core.Query{Query: "what did we discuss", SessionID: "current-empty", Limit: 5})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.Content != "we discussed the deploy plan" || hits[0].Provenance != crossSessionProvenance {
		t.Fatalf("hits = %#v, want the prior session's turn via cross-session fallback", hits)
	}
}

func TestRecallRawCrossSessionFallbackExcludesStaleEvents(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	manager := TieredManager{markdown: newTestMarkdownMemory(t, t.TempDir()), sqlite: store, events: &mockDispatcher{}}
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "prior", Role: "user", Content: "stale deployment detail", CreatedAt: time.Now().Add(-crossSessionRawMaxAge - time.Hour)}); err != nil {
		t.Fatal(err)
	}
	hits, err := manager.Recall(ctx, core.Query{Query: "deployment", SessionID: "current-empty", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("stale cross-session hits = %#v, want none", hits)
	}
}

func TestListHonorsUserScope(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"USER.md":   "- User prefers terse answers.\n",
		"MEMORY.md": "- Project uses markdown memory.\n",
	})

	records, err := manager.List(context.Background(), core.Query{
		Scopes: []config.MemoryScope{config.ScopeUser},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	assertRecords(t, records, []expectedRecord{
		{scope: config.ScopeUser, content: "User prefers terse answers."},
	})
}

func TestListHonorsProjectScope(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"USER.md":   "- User prefers terse answers.\n",
		"MEMORY.md": "- Project uses markdown memory.\n",
	})

	records, err := manager.List(context.Background(), core.Query{
		Scopes: []config.MemoryScope{config.ScopeProject},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	assertRecords(t, records, []expectedRecord{
		{scope: config.ScopeProject, content: "Project uses markdown memory."},
	})
}

func TestListDefaultsToUserAndProjectScopes(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"AGENTS.md": "- Agent rule stays prompt-only.\n",
		"USER.md":   "- User prefers terse answers.\n",
		"MEMORY.md": "- Project uses markdown memory.\n",
	})

	records, err := manager.List(context.Background(), core.Query{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	assertRecords(t, records, []expectedRecord{
		{scope: config.ScopeUser, content: "User prefers terse answers."},
		{scope: config.ScopeProject, content: "Project uses markdown memory."},
	})
}

func TestListHonorsLimitAfterScopeFiltering(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"USER.md":   "- First user record.\n- Second user record.\n",
		"MEMORY.md": "- Project record.\n",
	})

	records, err := manager.List(context.Background(), core.Query{
		Scopes: []config.MemoryScope{config.ScopeUser},
		Limit:  1,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	assertRecords(t, records, []expectedRecord{
		{scope: config.ScopeUser, content: "First user record."},
	})
}

func TestListUnsupportedScopeReturnsNoRecords(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"AGENTS.md": "- Agent rule stays prompt-only.\n",
		"USER.md":   "- User prefers terse answers.\n",
		"MEMORY.md": "- Project uses markdown memory.\n",
	})

	records, err := manager.List(context.Background(), core.Query{
		Scopes: []config.MemoryScope{config.ScopeSession},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records length = %d, want 0: %#v", len(records), records)
	}
}

func TestRecallPreservesProjectScopeConstraint(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"USER.md":   "- Shared keyword belongs to user.\n",
		"MEMORY.md": "- Shared keyword belongs to project.\n",
	})

	hits, err := manager.Recall(context.Background(), core.Query{
		Query:  "Shared keyword",
		Scopes: []config.MemoryScope{config.ScopeProject},
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits length = %d, want 1: %#v", len(hits), hits)
	}
	got := hits[0].Record
	if got.Scope != config.ScopeProject || !strings.Contains(got.Content, "belongs to project") {
		t.Fatalf("unexpected hit: %#v", got)
	}
}

func TestListDedupeSQLiteWinsOverMarkdown(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"MEMORY.md": "- Shared memory fact.\n",
	})

	saved, err := manager.Save(context.Background(), core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Shared memory fact.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	records, err := manager.List(context.Background(), core.Query{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("List() length = %d, want 1: %#v", len(records), records)
	}
	if records[0].ID != saved.ID || records[0].Source != core.SourceSQLite {
		t.Fatalf("sqlite record should win duplicate, got %#v want id %q", records[0], saved.ID)
	}
}

func TestMarkdownPinnedMemoryMatchesFactKindOnly(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"MEMORY.md": "- Pinned markdown memory.\n",
	})

	records, err := manager.List(context.Background(), core.Query{
		Scopes: []config.MemoryScope{config.ScopeProject},
		Kinds:  []core.Kind{core.KindPreference},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("List() = %#v, want no markdown preference records", records)
	}

	hits, err := manager.Recall(context.Background(), core.Query{
		Query:  "Pinned markdown",
		Scopes: []config.MemoryScope{config.ScopeProject},
		Kinds:  []core.Kind{core.KindFact},
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.Kind != core.KindFact || hits[0].Record.Source != core.SourceMarkdown {
		t.Fatalf("Recall() = %#v, want markdown fact hit", hits)
	}
}

func TestDedupeIgnoresKindSoSQLiteWinsOverUntypedMarkdown(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"MEMORY.md": "- Shared kindless memory.\n",
	})

	saved, err := manager.Save(context.Background(), core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindDecision,
		Content: "Shared kindless memory.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	records, err := manager.List(context.Background(), core.Query{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("List() length = %d, want 1: %#v", len(records), records)
	}
	if records[0].ID != saved.ID || records[0].Kind != core.KindDecision {
		t.Fatalf("sqlite record should win duplicate, got %#v want id %q", records[0], saved.ID)
	}
}

func TestUpdateUsesSQLiteAndFallsBackToMarkdown(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"MEMORY.md": "- Old markdown fact.\n",
	})

	saved, err := manager.Save(context.Background(), core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Old sqlite fact.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	updated, err := manager.Update(context.Background(), saved.ID, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Updated sqlite fact.",
	})
	if err != nil {
		t.Fatalf("Update(sqlite) error = %v", err)
	}
	if updated.ID != saved.ID || updated.Content != "Updated sqlite fact." {
		t.Fatalf("Update(sqlite) = %#v", updated)
	}
	hits, err := manager.Recall(context.Background(), core.Query{Query: "Updated sqlite", Limit: 5})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != saved.ID {
		t.Fatalf("Recall(updated sqlite) = %#v, want %q", hits, saved.ID)
	}

	md, err := manager.Update(context.Background(), "Old markdown fact.", core.Record{
		Scope:   config.ScopeProject,
		Content: "Updated markdown fact.",
	})
	if err != nil {
		t.Fatalf("Update(markdown) error = %v", err)
	}
	if md.Source != core.SourceMarkdown || md.ID != markdown.EntryID("Updated markdown fact.") {
		t.Fatalf("Update(markdown) = %#v", md)
	}
}

func TestUpdatePreservesRecordWhenEmbeddingFails(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	manager := TieredManager{
		sqlite: store,
		worker: &fakeMemoryWorker{vectors: [][]float32{{1, 0}}},
		events: &mockDispatcher{},
	}

	saved, err := manager.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindSummary,
		Source:  core.SourceRuntime,
		Content: "Vector rollback memory.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	manager.worker = failingEmbedWorker{}
	if _, err := manager.Update(ctx, saved.ID, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindSummary,
		Source:  core.SourceRuntime,
		Content: "Updated but embed fails.",
	}); err == nil {
		t.Fatal("Update() error = nil, want embed failure")
	}

	records, err := store.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeProject}, Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, rec := range records {
		if rec.ID == saved.ID && rec.Content == saved.Content {
			return
		}
	}
	t.Fatalf("record %q was not preserved after failed embedding: %#v", saved.ID, records)
}

func TestDeleteRemovesSQLiteAndMarkdownRecords(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"MEMORY.md": "- Delete markdown fact.\n",
	})

	saved, err := manager.Save(context.Background(), core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Delete sqlite fact.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := manager.Delete(context.Background(), saved.ID); err != nil {
		t.Fatalf("Delete(sqlite) error = %v", err)
	}
	if err := manager.Delete(context.Background(), "Delete markdown fact."); err != nil {
		t.Fatalf("Delete(markdown) error = %v", err)
	}

	records, err := manager.List(context.Background(), core.Query{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records after delete = %#v, want none", records)
	}
}

func TestPromptBlocksIgnoresHomeAgentsMarkdown(t *testing.T) {
	// The home AGENTS.md persona document is gone (merged into PERSONA.md,
	// which feeds the base prompt, not the memory blocks).
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"AGENTS.md": "- Agent rule.\n",
	})

	blocks, err := manager.PromptBlocks(context.Background(), core.PromptRequest{BudgetChars: 6000})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}

	assertPromptBlocks(t, blocks, nil)
}

func TestPromptBlocksReturnsUserBlocks(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"USER.md": "- User preference.\n",
	})

	blocks, err := manager.PromptBlocks(context.Background(), core.PromptRequest{BudgetChars: 6000})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}

	assertPromptBlocks(t, blocks, []expectedPromptBlock{
		{title: "User Memory", priority: 200, content: "User preference."},
	})
}

func TestPromptBlocksReturnsProjectBlocks(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"MEMORY.md": "- Project fact.\n",
	})

	blocks, err := manager.PromptBlocks(context.Background(), core.PromptRequest{BudgetChars: 6000})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}

	assertPromptBlocks(t, blocks, []expectedPromptBlock{
		{title: "Project Memory", priority: 100, content: "Project fact."},
	})
}

func TestPromptBlocksReturnsUserAndProjectBlocks(t *testing.T) {
	manager := newManagerWithMarkdownFiles(t, map[string]string{
		"USER.md":   "- User preference.\n",
		"MEMORY.md": "- Project fact.\n",
	})

	blocks, err := manager.PromptBlocks(context.Background(), core.PromptRequest{BudgetChars: 6000})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}

	assertPromptBlocks(t, blocks, []expectedPromptBlock{
		{title: "User Memory", priority: 200, content: "User preference."},
		{title: "Project Memory", priority: 100, content: "Project fact."},
	})
}

func TestHandleEventDoesNotAutoModifyAgentsMarkdown(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	memoryDir := filepath.Join(root, "notes")
	agentsPath := filepath.Join(memoryDir, "AGENTS.md")
	original := []byte("# Agent Rules\n\n- Existing procedural rule.\n")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := NewManagerFromConfigWithEvents(config.AppConfig{
		Memory: config.MemoryConfig{
			Dir: memoryDir,
		},
	}, &mockDispatcher{})
	if err != nil {
		t.Fatalf("NewManagerFromConfig() error = %v", err)
	}

	handler := NewEventHandler(manager)
	err = handler.HandleEvent(ctx, events.Event{
		Type: events.EventAfterResponse,
		Response: &events.ResponsePayload{
			Query: "Remember as an agent rule that this project should prefer small PRs.",
			Text:  "The project uses durable markdown memory.",
		},
	})
	if err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}

	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("expected AGENTS.md to be unchanged, got %q", string(data))
	}
}

type expectedPromptBlock struct {
	title    string
	priority int
	content  string
}

type expectedRecord struct {
	scope   config.MemoryScope
	content string
}

func newManagerWithMarkdownFiles(t *testing.T, files map[string]string) Manager {
	return newManagerWithMarkdownFilesAndEvents(t, files, &mockDispatcher{})
}

func newManagerWithMarkdownFilesAndEvents(t *testing.T, files map[string]string, dispatcher events.Dispatcher) Manager {
	t.Helper()
	root := t.TempDir()
	memoryDir := filepath.Join(root, "notes")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(memoryDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	manager, err := NewManagerFromConfigWithEvents(config.AppConfig{
		Memory: config.MemoryConfig{
			Dir: memoryDir,
			// The seeded files above use the flat legacy layout; pin
			// ProjectDir so MEMORY.md is read from the same directory.
			ProjectDir: memoryDir,
		},
	}, dispatcher)
	if err != nil {
		t.Fatalf("NewManagerFromConfig() error = %v", err)
	}
	return manager
}

func assertRecords(t *testing.T, records []core.Record, expected []expectedRecord) {
	t.Helper()
	if len(records) != len(expected) {
		t.Fatalf("records length = %d, want %d: %#v", len(records), len(expected), records)
	}
	for i, want := range expected {
		got := records[i]
		if got.Scope != want.scope {
			t.Fatalf("record %d scope = %q, want %q", i, got.Scope, want.scope)
		}
		if got.Source != core.SourceMarkdown {
			t.Fatalf("record %d source = %q, want %q", i, got.Source, core.SourceMarkdown)
		}
		if got.ID != markdown.EntryID(got.Content) {
			t.Fatalf("record %d ID = %q, want stable id for %q", i, got.ID, got.Content)
		}
		if !strings.Contains(got.Content, want.content) {
			t.Fatalf("record %d content missing %q: %q", i, want.content, got.Content)
		}
	}
}

func assertPromptBlocks(t *testing.T, blocks []core.PromptBlock, expected []expectedPromptBlock) {
	t.Helper()
	if len(blocks) != len(expected) {
		t.Fatalf("blocks length = %d, want %d: %#v", len(blocks), len(expected), blocks)
	}
	for i, want := range expected {
		got := blocks[i]
		if got.Title != want.title {
			t.Fatalf("block %d title = %q, want %q", i, got.Title, want.title)
		}
		if got.Priority != want.priority {
			t.Fatalf("block %d priority = %d, want %d", i, got.Priority, want.priority)
		}
		if got.Source != core.SourceMarkdown {
			t.Fatalf("block %d source = %q, want %q", i, got.Source, core.SourceMarkdown)
		}
		if !strings.Contains(got.Content, want.content) {
			t.Fatalf("block %d content missing %q: %q", i, want.content, got.Content)
		}
	}
}
