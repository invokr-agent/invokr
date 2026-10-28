package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
)

func newIssue299Manager(t *testing.T) TieredManager {
	t.Helper()
	return TieredManager{
		markdown: newTestMarkdownMemory(t, t.TempDir()),
		sqlite:   newTestSQLiteStore(t),
		events:   &mockDispatcher{},
	}
}

// Defect 1: an assistant reply that happens to be a numbered list mentioning
// "project"/"memory" must not materialise a project markdown record.
func TestHandleEventNeverCuratesAssistantReplyText(t *testing.T) {
	ctx := context.Background()
	manager := newIssue299Manager(t)

	err := manager.HandleEvent(ctx, events.Event{
		Type:      events.EventAfterResponse,
		SessionID: "s1",
		Response: &events.ResponsePayload{
			Query: "List three reasons to keep project memory small",
			Text: "1) instead of the project-scoped record we just updated\n" +
				"2) the project memory file stays small\n" +
				"3) the repository record set stays reviewable",
		},
	})
	if err != nil {
		t.Fatalf("HandleEvent() error = %v", err)
	}

	entries, err := manager.markdown.Project().List(ctx)
	if err != nil {
		t.Fatalf("List project memory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("assistant prose was curated into project memory: %#v", entries)
	}
	records, err := manager.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeProject}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("phantom records = %#v, want none", records)
	}
}

// Defect 1: every listed record has an id distinct from its content, and
// deleting by that id actually removes it — markdown records included.
func TestMarkdownRecordIDIsDeletable(t *testing.T) {
	ctx := context.Background()
	manager := newIssue299Manager(t)

	saved, err := manager.Save(ctx, core.Record{
		Scope: config.ScopeProject, Kind: core.KindFact,
		Source: core.SourceMarkdown, Content: "mem-canary-proj-11",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if saved.ID == saved.Content {
		t.Fatalf("markdown record id %q equals its content", saved.ID)
	}

	records, err := manager.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeProject}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 || records[0].ID != saved.ID {
		t.Fatalf("records = %#v, want the saved record under id %q", records, saved.ID)
	}

	if err := manager.Delete(ctx, saved.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	records, err = manager.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeProject}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records after delete = %#v, want none", records)
	}
	entries, err := manager.markdown.Project().List(ctx)
	if err != nil {
		t.Fatalf("List project memory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("MEMORY.md after delete = %#v, want empty", entries)
	}
}

// Defect 2: update merges onto the stored record — kind, scope and created_at
// survive an update that names only id and content.
func TestUpdateMergesOntoStoredRecord(t *testing.T) {
	ctx := context.Background()
	manager := newIssue299Manager(t)

	saved, err := manager.Save(ctx, core.Record{
		Scope: config.ScopeProject, Kind: core.KindDecision,
		Source: core.SourceRuntime, Content: "mem-canary-proj-11",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	updated, err := manager.Update(ctx, saved.ID, core.Record{Content: "mem-canary-proj-11-v2"})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Kind != core.KindDecision || updated.Scope != config.ScopeProject {
		t.Fatalf("update response = %#v, want the stored kind/scope preserved", updated)
	}
	if updated.Content != "mem-canary-proj-11-v2" {
		t.Fatalf("content = %q, want the new content", updated.Content)
	}
	if updated.CreatedAt.IsZero() || !updated.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("created_at = %v, want the stored %v", updated.CreatedAt, saved.CreatedAt)
	}

	records, err := manager.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeProject}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 || records[0].Kind != core.KindDecision || records[0].Content != "mem-canary-proj-11-v2" {
		t.Fatalf("records = %#v, want the merged record on disk", records)
	}
}

// Defect 2: an explicit kind still wins, and an unknown id is an error naming it.
func TestUpdateExplicitKindAndUnknownID(t *testing.T) {
	ctx := context.Background()
	manager := newIssue299Manager(t)

	saved, err := manager.Save(ctx, core.Record{
		Scope: config.ScopeProject, Kind: core.KindDecision,
		Source: core.SourceRuntime, Content: "mem-canary-proj-11",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	updated, err := manager.Update(ctx, saved.ID, core.Record{Kind: core.KindFact})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Kind != core.KindFact {
		t.Fatalf("kind = %q, want an explicit kind to win", updated.Kind)
	}

	_, err = manager.Update(ctx, "no-such-id", core.Record{Content: "whatever"})
	if err == nil || !strings.Contains(err.Error(), "no-such-id") {
		t.Fatalf("Update(unknown) error = %v, want an error naming the id", err)
	}
}

// Defect 3: recall surfaces saved memory_records, not only transcript turns,
// even when the caller defaults session_id to the current session.
func TestRecallSurfacesMemoryRecords(t *testing.T) {
	ctx := context.Background()
	manager := newIssue299Manager(t)

	for _, scope := range []config.MemoryScope{config.ScopeUser, config.ScopeProject} {
		if _, err := manager.Save(ctx, core.Record{
			Scope: scope, Kind: core.KindDecision, Source: core.SourceRuntime,
			Content: "mem-canary-" + string(scope) + " memcanary11",
		}); err != nil {
			t.Fatalf("Save(%s) error = %v", scope, err)
		}
	}
	if _, err := manager.sqlite.AppendMemoryEvent(ctx, core.MemoryEventRecord{
		SessionID: "s1", Role: "user", Content: "memcanary11 mentioned in chat",
	}); err != nil {
		t.Fatalf("AppendMemoryEvent() error = %v", err)
	}

	hits, err := manager.Recall(ctx, core.Query{Query: "memcanary11", SessionID: "s1", Limit: 5})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	scopes := map[config.MemoryScope]bool{}
	for _, hit := range hits {
		scopes[hit.Record.Scope] = true
	}
	if !scopes[config.ScopeUser] || !scopes[config.ScopeProject] {
		t.Fatalf("hits = %#v, want both the user- and project-scoped records", hits)
	}
}
