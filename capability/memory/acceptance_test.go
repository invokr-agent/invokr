package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/summary"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
)

func TestMemoryAcceptance_MarkdownScenarios(t *testing.T) {
	ctx := context.Background()
	memoryDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORY.md"), []byte("- Existing project fact.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := newAcceptanceManager(t, memoryDir, nil, nil)
	defer func() { _ = manager.Close() }()

	if _, err := manager.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Source: core.SourceMarkdown, Content: "Saved project fact."}); err != nil {
		t.Fatalf("Save(project) error = %v", err)
	}
	if _, err := manager.Save(ctx, core.Record{Scope: config.ScopeUser, Kind: core.KindFact, Source: core.SourceMarkdown, Content: "Saved user fact."}); err != nil {
		t.Fatalf("Save(user) error = %v", err)
	}
	assertFileContains(t, filepath.Join(memoryDir, "MEMORY.md"), "Saved project fact.")
	assertFileContains(t, filepath.Join(memoryDir, "USER.md"), "Saved user fact.")

	records, err := manager.List(ctx, core.Query{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	assertRecordsContain(t, records, config.ScopeProject, "Existing project fact.")
	assertRecordsContain(t, records, config.ScopeProject, "Saved project fact.")
	assertRecordsContain(t, records, config.ScopeUser, "Saved user fact.")

	blocks, err := manager.PromptBlocks(ctx, core.PromptRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}
	if !promptBlocksContain(blocks, "Saved project fact.") || !promptBlocksContain(blocks, "Saved user fact.") {
		t.Fatalf("prompt blocks missing markdown facts: %#v", blocks)
	}
}

func TestMemoryAcceptance_RuntimeScenarios(t *testing.T) {
	ctx := context.Background()
	manager := newAcceptanceManager(t, t.TempDir(), nil, nil)
	defer func() { _ = manager.Close() }()

	user, err := manager.Save(ctx, core.Record{Scope: config.ScopeUser, Kind: core.KindFact, Source: core.SourceRuntime, Content: "runtime user alpha"})
	if err != nil {
		t.Fatalf("Save(user) error = %v", err)
	}
	project, err := manager.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Source: core.SourceRuntime, Content: "runtime project alpha"})
	if err != nil {
		t.Fatalf("Save(project) error = %v", err)
	}
	if user.ID == "" || project.ID == "" {
		t.Fatalf("runtime IDs are empty: user=%#v project=%#v", user, project)
	}

	hits, err := manager.Recall(ctx, core.Query{Query: "alpha", Scopes: []config.MemoryScope{config.ScopeProject}, Limit: 5})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != project.ID {
		t.Fatalf("Recall(project alpha) = %#v, want project %q", hits, project.ID)
	}

	records, err := manager.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeUser}, Limit: 1})
	if err != nil {
		t.Fatalf("List(user) error = %v", err)
	}
	if len(records) != 1 || records[0].ID != user.ID {
		t.Fatalf("List(user) = %#v, want user %q", records, user.ID)
	}
}

func TestMemoryAcceptance_RecallAndSaveEvents(t *testing.T) {
	ctx := context.Background()
	var got []events.Event
	dispatcher := events.NewOrderedDispatcher(nil, events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		got = append(got, ev)
		return nil
	}))
	manager := newAcceptanceManager(t, t.TempDir(), dispatcher, nil)
	defer func() { _ = manager.Close() }()

	if _, err := manager.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Source: core.SourceRuntime, Content: "eventful memory"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := manager.Recall(ctx, core.Query{Query: "eventful", Limit: 3}); err != nil {
		t.Fatalf("Recall() error = %v", err)
	}

	assertEventTypes(t, got, []events.EventType{
		events.EventBeforeSave,
		events.EventAfterSave,
		events.EventBeforeRecall,
		events.EventAfterRecall,
	})
	if got[3].Recall == nil || len(got[3].Recall.Hits) == 0 {
		t.Fatalf("after recall payload missing hits: %#v", got[3])
	}
}

// TestMemoryAcceptance_VectorAndSummarizationThroughWorker exercises the vector
// save/recall and summarization-on-event flow using an in-process fake worker
// (no llama-server, no HTTP). The real Zerfoo path is covered by the
// integration tests (see worker_integration_test.go).
func TestMemoryAcceptance_VectorAndSummarizationThroughWorker(t *testing.T) {
	ctx := context.Background()

	worker := &fakeMemoryWorker{
		vectors: [][]float32{{1, 0}},
		summary: summary.MemorySummary{
			Summary:     "Project stores HTTP summaries.",
			Facts:       []summary.MemoryFact{{Subject: "project", Predicate: "stores", Object: "HTTP summaries", Confidence: 0.9}},
			Topics:      []string{"memory"},
			Importance:  0.9,
			ShouldStore: true,
		},
	}
	manager := newAcceptanceManager(t, t.TempDir(), nil, worker)
	defer func() { _ = manager.Close() }()

	saved, err := manager.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindSummary, Source: core.SourceRuntime, Content: "Semantic vector memory."})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	hits, err := manager.Recall(ctx, core.Query{Query: "semantic", Limit: 1})
	if err != nil {
		t.Fatalf("Recall(vector) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != saved.ID {
		t.Fatalf("Recall(vector) = %#v, want %q", hits, saved.ID)
	}

	handler := NewEventHandler(manager)
	if err := handler.HandleEvent(ctx, events.Event{
		Type:      events.EventAfterResponse,
		SessionID: "s1",
		Response:  &events.ResponsePayload{Query: "remember HTTP summaries", Text: "project stores HTTP summaries"},
	}); err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}
	// Summarization runs on the background runner; drain it before asserting.
	if tm, ok := manager.(TieredManager); ok && tm.curator != nil {
		_ = tm.curator.Close()
	}
	records, err := manager.List(ctx, core.Query{Query: "HTTP summaries", Limit: 10})
	if err != nil {
		t.Fatalf("List(summary) error = %v", err)
	}
	assertRecordsContain(t, records, config.ScopeProject, "Project stores HTTP summaries.")
	assertRecordsContain(t, records, config.ScopeProject, "project stores HTTP summaries")
	if worker.summaryCalls == 0 {
		t.Fatal("expected summarization to run on event threshold")
	}
}

// TestMemoryAcceptance_ForgettingArchivesStaleKeepsRecalled exercises the full
// manager wiring: a never-recalled runtime record decays below threshold and is
// archived (gone from recall), a recalled record is reinforced and survives, and
// an audit event names the archived record. MinRetention is set above 1.0 so a
// fresh strength-1.0 record is below threshold while a reinforced one is not —
// this makes archiving deterministic without manipulating time.
func TestMemoryAcceptance_ForgettingArchivesStaleKeepsRecalled(t *testing.T) {
	ctx := context.Background()
	var got []events.Event
	dispatcher := events.NewOrderedDispatcher(nil, events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		got = append(got, ev)
		return nil
	}))
	manager := newForgettingManager(t, t.TempDir(), dispatcher, config.ForgettingConfig{
		Enabled: true, HalfLifeDays: 14, MinRetention: 2.0, ReinforcementFactor: 3.0, MaxStrength: 10.0,
	})
	defer func() { _ = manager.Close() }()

	survivor := saveRuntimeFact(t, manager, "keepme alpha")
	stale := saveRuntimeFact(t, manager, "forgetme alpha")

	// Recall reinforces the survivor (strength 1.0 -> 3.0), lifting it above the
	// 2.0 retention floor; the stale record is never recalled.
	if _, err := manager.Recall(ctx, core.Query{Query: "keepme", Limit: 5}); err != nil {
		t.Fatalf("Recall(keepme) error = %v", err)
	}

	tm := manager.(TieredManager)
	tm.sweep(ctx)

	// Stale record is archived: gone from recall, reachable only via the archive.
	hits, err := manager.Recall(ctx, core.Query{Query: "alpha", Limit: 5})
	if err != nil {
		t.Fatalf("Recall(alpha) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != survivor.ID {
		t.Fatalf("Recall(alpha) = %#v, want only survivor %q", hits, survivor.ID)
	}
	archived, err := tm.sqlite.SearchArchive(ctx, "forgetme", 10)
	if err != nil {
		t.Fatalf("SearchArchive() error = %v", err)
	}
	if len(archived) != 1 || archived[0].ID != stale.ID {
		t.Fatalf("SearchArchive() = %#v, want archived stale %q", archived, stale.ID)
	}
	assertForgetAudit(t, got, 1)
}

// TestMemoryAcceptance_ForgettingDisabledIsInert verifies that with forgetting
// off, recall neither reinforces nor archives and emits no audit event —
// behavior identical to the pre-forgetting store.
func TestMemoryAcceptance_ForgettingDisabledIsInert(t *testing.T) {
	ctx := context.Background()
	var got []events.Event
	dispatcher := events.NewOrderedDispatcher(nil, events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		got = append(got, ev)
		return nil
	}))
	manager := newForgettingManager(t, t.TempDir(), dispatcher, config.ForgettingConfig{
		Enabled: false, MinRetention: 2.0, ReinforcementFactor: 3.0,
	})
	defer func() { _ = manager.Close() }()

	survivor := saveRuntimeFact(t, manager, "keepme alpha")
	stale := saveRuntimeFact(t, manager, "forgetme alpha")

	if _, err := manager.Recall(ctx, core.Query{Query: "keepme", Limit: 5}); err != nil {
		t.Fatalf("Recall(keepme) error = %v", err)
	}
	tm := manager.(TieredManager)
	tm.sweep(ctx)

	hits, err := manager.Recall(ctx, core.Query{Query: "alpha", Limit: 5})
	if err != nil {
		t.Fatalf("Recall(alpha) error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("Recall(alpha) = %d hits, want 2 (nothing archived)", len(hits))
	}
	// No reinforcement: every candidate keeps strength 1.0 (no column churn).
	cands, err := tm.sqlite.SweepCandidates(ctx)
	if err != nil {
		t.Fatalf("SweepCandidates() error = %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("SweepCandidates() = %d, want 2", len(cands))
	}
	for _, c := range cands {
		if c.Strength != 1.0 || c.RecallCount != 0 {
			t.Fatalf("disabled recall churned retention columns: %#v", c)
		}
	}
	_ = survivor
	_ = stale
	assertForgetAudit(t, got, 0)
}

func saveRuntimeFact(t *testing.T, manager Manager, content string) core.Record {
	t.Helper()
	rec, err := manager.Save(context.Background(), core.Record{
		Scope: config.ScopeProject, Kind: core.KindFact, Source: core.SourceRuntime, Content: content,
	})
	if err != nil {
		t.Fatalf("Save(%q) error = %v", content, err)
	}
	return rec
}

// assertForgetAudit asserts exactly wantArchived records were named in a
// memory_forget audit event (0 means no such event should exist).
func assertForgetAudit(t *testing.T, got []events.Event, wantArchived int) {
	t.Helper()
	for _, ev := range got {
		if ev.Type != events.EventAudit || ev.Audit == nil || ev.Audit.Action != "memory_forget" {
			continue
		}
		if wantArchived == 0 {
			t.Fatalf("unexpected memory_forget audit: %#v", ev.Audit)
		}
		if n, _ := ev.Audit.Details["archived_count"].(int); n != wantArchived {
			t.Fatalf("audit archived_count = %v, want %d", ev.Audit.Details["archived_count"], wantArchived)
		}
		return
	}
	if wantArchived != 0 {
		t.Fatalf("no memory_forget audit event in %#v", got)
	}
}

func newForgettingManager(t *testing.T, memoryDir string, dispatcher events.Dispatcher, fc config.ForgettingConfig) Manager {
	t.Helper()
	if dispatcher == nil {
		dispatcher = events.NewOrderedDispatcher(nil)
	}
	manager, err := NewManagerFromConfigWithEvents(config.AppConfig{
		Memory: config.MemoryConfig{Dir: memoryDir, Forgetting: fc},
	}, dispatcher)
	if err != nil {
		t.Fatalf("new forgetting manager: %v", err)
	}
	return manager
}

func newAcceptanceManager(t *testing.T, memoryDir string, dispatcher events.Dispatcher, worker memoryWorker) Manager {
	t.Helper()
	if dispatcher == nil {
		dispatcher = events.NewOrderedDispatcher(nil)
	}
	manager, err := newManagerFromConfigWithEvents(config.AppConfig{
		Memory: config.MemoryConfig{
			Dir: memoryDir,
			// Acceptance scenarios seed MEMORY.md next to the other
			// documents; pin ProjectDir to keep the flat layout.
			ProjectDir: memoryDir,
			Zerfoo: config.ZerfooMemoryConfig{
				MaxRawEventsBeforeSummary: 2,
				MinImportanceToStore:      0.1,
			},
		},
	}, dispatcher, worker, nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func assertFileContains(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s missing %q: %q", path, want, data)
	}
}

func assertRecordsContain(t *testing.T, records []core.Record, scope config.MemoryScope, content string) {
	t.Helper()
	for _, rec := range records {
		if rec.Scope == scope && strings.Contains(rec.Content, content) {
			return
		}
	}
	t.Fatalf("records missing scope=%q content=%q: %#v", scope, content, records)
}

func promptBlocksContain(blocks []core.PromptBlock, content string) bool {
	for _, block := range blocks {
		if strings.Contains(block.Content, content) {
			return true
		}
	}
	return false
}

func assertEventTypes(t *testing.T, got []events.Event, want []events.EventType) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Type != want[i] {
			t.Fatalf("event %d = %q, want %q: %#v", i, got[i].Type, want[i], got)
		}
	}
}
