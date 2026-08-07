package sqlite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/mathutil"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *Memory {
	t.Helper()

	store, err := NewStore(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return store
}

func TestStoreSaveListAndDedupe(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	first, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Invokr uses SQLite FTS for literal recall.",
		Summary: "SQLite FTS literal recall",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if first.ID == "" {
		t.Fatalf("Save() ID is empty")
	}
	if first.Source != core.SourceSQLite {
		t.Fatalf("Save() Source = %q, want %q", first.Source, core.SourceSQLite)
	}

	dupe, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Invokr uses SQLite FTS for literal recall.",
	})
	if err != nil {
		t.Fatalf("Save() duplicate error = %v", err)
	}
	if dupe.ID != first.ID {
		t.Fatalf("duplicate ID = %q, want %q", dupe.ID, first.ID)
	}

	records, err := store.List(ctx, core.Query{
		Scopes: []config.MemoryScope{config.ScopeProject},
		Kinds:  []core.Kind{core.KindFact},
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("List() len = %d, want 1: %#v", len(records), records)
	}
	if records[0].Content != first.Content {
		t.Fatalf("List()[0].Content = %q, want %q", records[0].Content, first.Content)
	}
}

func TestStoreSaveDoesNotMaskNonUniqueInsertError(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rec := core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "existing duplicate"}
	if _, err := store.Save(ctx, rec); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER fail_memory_insert
		BEFORE INSERT ON memory_records
		BEGIN
			SELECT RAISE(FAIL, 'forced insert failure');
		END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, err := store.Save(ctx, rec)
	if err == nil || !strings.Contains(err.Error(), "forced insert failure") {
		t.Fatalf("Save() error = %v, want forced insert failure", err)
	}
}

func TestStoreRecallFTSHonorsFilters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	project, err := store.Save(ctx, core.Record{
		Scope:     config.ScopeProject,
		Kind:      core.KindDecision,
		Content:   "Decision: keep Tier 1 as literal recall with SQLite FTS.",
		SessionID: "session-a",
	})
	if err != nil {
		t.Fatalf("Save(project) error = %v", err)
	}
	if _, err := store.Save(ctx, core.Record{
		Scope:     config.ScopeUser,
		Kind:      core.KindPreference,
		Content:   "User prefers terse memory replies.",
		SessionID: "session-b",
	}); err != nil {
		t.Fatalf("Save(user) error = %v", err)
	}

	hits, err := store.RecallFTS(ctx, core.Query{
		Query:     "literal recall",
		Scopes:    []config.MemoryScope{config.ScopeProject},
		Kinds:     []core.Kind{core.KindDecision},
		SessionID: "session-a",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("RecallFTS() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("RecallFTS() len = %d, want 1: %#v", len(hits), hits)
	}
	if hits[0].Record.ID != project.ID {
		t.Fatalf("RecallFTS()[0].ID = %q, want %q", hits[0].Record.ID, project.ID)
	}

	hits, err = store.RecallFTS(ctx, core.Query{
		Query:  "literal recall",
		Scopes: []config.MemoryScope{config.ScopeUser},
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("RecallFTS(user) error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("RecallFTS(user) = %#v, want no hits", hits)
	}
}

func TestStoreRecallVectorOrdersByCosineAndHonorsFilters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	closeRec, err := store.Save(ctx, core.Record{
		Scope:     config.ScopeProject,
		Kind:      core.KindDecision,
		Content:   "Vector memory about llama recall.",
		SessionID: "session-a",
	})
	if err != nil {
		t.Fatalf("Save(close) error = %v", err)
	}
	farRec, err := store.Save(ctx, core.Record{
		Scope:     config.ScopeProject,
		Kind:      core.KindDecision,
		Content:   "Vector memory about unrelated tools.",
		SessionID: "session-a",
	})
	if err != nil {
		t.Fatalf("Save(far) error = %v", err)
	}
	if err := store.SaveVector(ctx, closeRec.ID, []float32{1, 0}); err != nil {
		t.Fatalf("SaveVector(close) error = %v", err)
	}
	if err := store.SaveVector(ctx, farRec.ID, []float32{0, 1}); err != nil {
		t.Fatalf("SaveVector(far) error = %v", err)
	}

	hits, err := store.RecallVector(ctx, []float32{1, 0}, core.Query{
		Scopes:    []config.MemoryScope{config.ScopeProject},
		Kinds:     []core.Kind{core.KindDecision},
		SessionID: "session-a",
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("RecallVector() error = %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("RecallVector() len = %d, want 2: %#v", len(hits), hits)
	}
	if hits[0].Record.ID != closeRec.ID || hits[0].Score <= hits[1].Score {
		t.Fatalf("RecallVector() order = %#v, want close record first", hits)
	}

	hits, err = store.RecallVector(ctx, []float32{1, 0}, core.Query{
		Scopes: []config.MemoryScope{config.ScopeUser},
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("RecallVector(wrong scope) error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("RecallVector(wrong scope) = %#v, want no hits", hits)
	}
}

func TestStoreRecallVectorTreatsSessionIDAsPreference(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	project, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindDecision,
		Content: "Project-scoped vector memory without a session.",
	})
	if err != nil {
		t.Fatalf("Save(project) error = %v", err)
	}
	if err := store.SaveVector(ctx, project.ID, []float32{1, 0}); err != nil {
		t.Fatalf("SaveVector(project) error = %v", err)
	}
	other, err := store.Save(ctx, core.Record{
		Scope:     config.ScopeSession,
		Kind:      core.KindDecision,
		Content:   "Session-scoped vector memory for another session.",
		SessionID: "session-b",
	})
	if err != nil {
		t.Fatalf("Save(other) error = %v", err)
	}
	if err := store.SaveVector(ctx, other.ID, []float32{1, 0}); err != nil {
		t.Fatalf("SaveVector(other) error = %v", err)
	}

	hits, err := store.RecallVector(ctx, []float32{1, 0}, core.Query{
		SessionID: "session-a",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("RecallVector() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("RecallVector() len = %d, want 1: %#v", len(hits), hits)
	}
	if hits[0].Record.ID != project.ID {
		t.Fatalf("RecallVector()[0].ID = %q, want session-less project record %q", hits[0].Record.ID, project.ID)
	}
}

func TestStoreSaveVectorUpdatesExistingVectorAndLimitsResults(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	first, err := store.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "First vector memory."})
	if err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}
	second, err := store.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "Second vector memory."})
	if err != nil {
		t.Fatalf("Save(second) error = %v", err)
	}
	if err := store.SaveVector(ctx, first.ID, []float32{0, 1}); err != nil {
		t.Fatalf("SaveVector(first old) error = %v", err)
	}
	if err := store.SaveVector(ctx, first.ID, []float32{1, 0}); err != nil {
		t.Fatalf("SaveVector(first new) error = %v", err)
	}
	if err := store.SaveVector(ctx, second.ID, []float32{0.9, 0.1}); err != nil {
		t.Fatalf("SaveVector(second) error = %v", err)
	}

	hits, err := store.RecallVector(ctx, []float32{1, 0}, core.Query{Limit: 1})
	if err != nil {
		t.Fatalf("RecallVector() error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("RecallVector() len = %d, want 1: %#v", len(hits), hits)
	}
	if hits[0].Record.ID != first.ID {
		t.Fatalf("RecallVector() first hit = %q, want updated first vector %q", hits[0].Record.ID, first.ID)
	}
}

func TestStoreRecallVectorScoresMismatchedDimensionsAsZero(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rec, err := store.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "Mismatched vector memory."})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.SaveVector(ctx, rec.ID, []float32{1, 0, 0}); err != nil {
		t.Fatalf("SaveVector() error = %v", err)
	}

	hits, err := store.RecallVector(ctx, []float32{1, 0}, core.Query{Limit: 1})
	if err != nil {
		t.Fatalf("RecallVector() error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("RecallVector() = %#v, want mismatched dimensions filtered out", hits)
	}
}

func TestVectorCandidateLimitIsBounded(t *testing.T) {
	tests := []struct {
		limit int
		want  int
	}{
		{limit: 1, want: vectorCandidateFloor},
		{limit: 20, want: 400},
		{limit: 1000, want: vectorCandidateCap},
	}
	for _, tt := range tests {
		if got := vectorCandidateLimit(tt.limit); got != tt.want {
			t.Fatalf("vectorCandidateLimit(%d) = %d, want %d", tt.limit, got, tt.want)
		}
	}
}

func TestStoreRecallVectorReadsLegacyJSONRows(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	rec, err := store.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "Legacy JSON vector memory."})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	now := core.UTCNow()
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO memory_vectors(record_uuid, vector_json, created_at, updated_at)
		VALUES (?, ?, ?, ?)
	`, rec.ID, `[1,0]`, now, now); err != nil {
		t.Fatalf("insert legacy vector row: %v", err)
	}

	hits, err := store.RecallVector(ctx, []float32{1, 0}, core.Query{Limit: 1})
	if err != nil {
		t.Fatalf("RecallVector() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != rec.ID || hits[0].Score != 1 {
		t.Fatalf("RecallVector() = %#v, want legacy row score 1", hits)
	}
}

func TestVectorBlobRoundTrip(t *testing.T) {
	vector := []float32{1.5, -2.25, 0.125}
	blob, norm, err := encodeVector(vector)
	if err != nil {
		t.Fatalf("encodeVector() error = %v", err)
	}
	if len(blob) != len(vector)*4 {
		t.Fatalf("blob len = %d, want %d", len(blob), len(vector)*4)
	}
	if math.Abs(norm-mathutil.Norm(vector)) > 0.000001 {
		t.Fatalf("norm = %v, want %v", norm, mathutil.Norm(vector))
	}
	got, err := decodeVector(blob)
	if err != nil {
		t.Fatalf("decodeVector() error = %v", err)
	}
	if len(got) != len(vector) {
		t.Fatalf("decoded len = %d, want %d", len(got), len(vector))
	}
	for i := range vector {
		if got[i] != vector[i] {
			t.Fatalf("decoded[%d] = %v, want %v", i, got[i], vector[i])
		}
	}

	if _, err := decodeVector([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected malformed vector blob error")
	}
}

func TestStoreRawMemoryEventsAppendRecentAndCount(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "s1", Role: "user", Content: "first"}); err != nil {
		t.Fatalf("AppendMemoryEvent(first) error = %v", err)
	}
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "s1", Role: "assistant", Content: "second"}); err != nil {
		t.Fatalf("AppendMemoryEvent(second) error = %v", err)
	}
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "s2", Role: "user", Content: "other"}); err != nil {
		t.Fatalf("AppendMemoryEvent(other) error = %v", err)
	}

	count, err := store.CountMemoryEvents(ctx, "s1")
	if err != nil {
		t.Fatalf("CountMemoryEvents() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	events, err := store.RecentMemoryEvents(ctx, "s1", 10)
	if err != nil {
		t.Fatalf("RecentMemoryEvents() error = %v", err)
	}
	if len(events) != 2 || events[0].Content != "first" || events[1].Content != "second" {
		t.Fatalf("events = %#v, want append order", events)
	}
}

func TestStoreListUsesFTSForQueryAndHonorsFilters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	project, err := store.Save(ctx, core.Record{
		Scope:     config.ScopeProject,
		Kind:      core.KindDecision,
		Content:   "Decision: SQLite FTS backs list queries.",
		SessionID: "session-a",
	})
	if err != nil {
		t.Fatalf("Save(project) error = %v", err)
	}
	if _, err := store.Save(ctx, core.Record{
		Scope:     config.ScopeUser,
		Kind:      core.KindPreference,
		Content:   "User prefers SQLite FTS examples.",
		SessionID: "session-b",
	}); err != nil {
		t.Fatalf("Save(user) error = %v", err)
	}

	records, err := store.List(ctx, core.Query{
		Query:     "list queries",
		Scopes:    []config.MemoryScope{config.ScopeProject},
		Kinds:     []core.Kind{core.KindDecision},
		SessionID: "session-a",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 || records[0].ID != project.ID {
		t.Fatalf("List() = %#v, want project record %q", records, project.ID)
	}

	records, err = store.List(ctx, core.Query{
		Query:  "SQLite FTS",
		Scopes: []config.MemoryScope{config.ScopeProject},
		Kinds:  []core.Kind{core.KindPreference},
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("List(wrong kind) error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("List(wrong kind) = %#v, want no records", records)
	}
}

func TestStoreRecallFTSHandlesPunctuation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	rec, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Invokr supports full-text recall for package names like github.com/acme/repo.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	hits, err := store.RecallFTS(ctx, core.Query{
		Query: "full-text",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("RecallFTS(full-text) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != rec.ID {
		t.Fatalf("RecallFTS(full-text) = %#v, want record %q", hits, rec.ID)
	}

	hits, err = store.RecallFTS(ctx, core.Query{
		Query: "github.com/acme/repo",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("RecallFTS(package path) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != rec.ID {
		t.Fatalf("RecallFTS(package path) = %#v, want record %q", hits, rec.ID)
	}
}

func TestStoreUpdateRefreshesFTS(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	rec, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Old sqlite fact.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	updated, err := store.Update(ctx, rec.ID, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Updated sqlite fact.",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.ID != rec.ID {
		t.Fatalf("Update() ID = %q, want %q", updated.ID, rec.ID)
	}

	hits, err := store.RecallFTS(ctx, core.Query{Query: "updated", Limit: 5})
	if err != nil {
		t.Fatalf("RecallFTS(updated) error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != rec.ID {
		t.Fatalf("RecallFTS(updated) = %#v, want record %q", hits, rec.ID)
	}

	hits, err = store.RecallFTS(ctx, core.Query{Query: "old", Limit: 5})
	if err != nil {
		t.Fatalf("RecallFTS(old) error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("RecallFTS(old) = %#v, want no hits", hits)
	}
}

func TestStoreUpdateWithVectorRollsBackBothWrites(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rec, err := store.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "Original memory."})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.SaveVector(ctx, rec.ID, []float32{1, 0}); err != nil {
		t.Fatalf("SaveVector() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_vector_update BEFORE UPDATE ON memory_vectors BEGIN SELECT RAISE(ABORT, 'vector rejected'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, err = store.UpdateWithVector(ctx, rec.ID, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "Changed memory."}, []float32{0, 1})
	if err == nil {
		t.Fatal("UpdateWithVector() error = nil, want vector failure")
	}
	hits, err := store.RecallFTS(ctx, core.Query{Query: "original", Limit: 5})
	if err != nil {
		t.Fatalf("RecallFTS() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Record.ID != rec.ID || hits[0].Record.Content != rec.Content {
		t.Fatalf("record after rollback = %#v, want original %#v", hits, rec)
	}
}

func TestStoreDeleteRemovesRecordFromRecall(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	rec, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeSession,
		Kind:    core.KindEpisode,
		Content: "Command failed: go test ./... returned timeout.",
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.Delete(ctx, rec.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	hits, err := store.RecallFTS(ctx, core.Query{Query: "timeout", Limit: 5})
	if err != nil {
		t.Fatalf("RecallFTS() error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("RecallFTS() after Delete = %#v, want no hits", hits)
	}

	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM memory_records WHERE uuid = ?`, rec.ID).Scan(&status); err != nil {
		t.Fatalf("query status error = %v", err)
	}
	if status != statusDeleted {
		t.Fatalf("status = %q, want %q", status, statusDeleted)
	}
}

func TestStoreSerializesConcurrentReadsAndWrites(t *testing.T) {
	store, err := NewStore(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	records := make([]core.Record, 8)
	for i := range records {
		records[i], err = store.Save(context.Background(), core.Record{Content: fmt.Sprintf("parallel canary %d", i)})
		if err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	errCh := make(chan error, len(records)+1)
	for _, record := range records {
		go func() { errCh <- store.Delete(context.Background(), record.ID) }()
	}
	go func() {
		_, listErr := store.List(context.Background(), core.Query{Query: "parallel"})
		errCh <- listErr
	}()
	for range cap(errCh) {
		if concurrentErr := <-errCh; concurrentErr != nil {
			t.Fatalf("concurrent operation error = %v", concurrentErr)
		}
	}
}

func TestOpenStoreAtPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "memory")

	store, err := NewStore(Config{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	if err := ensureRequiredTables(store.db); err != nil {
		t.Fatalf("ensureRequiredTables() error = %v", err)
	}
}

func TestStoreDocKindHiddenByDefaultAndMetadataRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	doc, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindDoc,
		Content: "Gateway memory module notes live in docs.",
		Metadata: map[string]string{
			"path":        "docs/architecture.md",
			"source_hash": "sha256:abc",
			"file_size":   "42",
		},
	})
	if err != nil {
		t.Fatalf("Save() doc error = %v", err)
	}
	if _, err := store.Save(ctx, core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Content: "Gateway memory module fact.",
	}); err != nil {
		t.Fatalf("Save() fact error = %v", err)
	}

	// Default (kind-less) recall and list must exclude doc records.
	hits, err := store.RecallFTS(ctx, core.Query{Query: "gateway memory module"})
	if err != nil {
		t.Fatalf("RecallFTS() error = %v", err)
	}
	for _, hit := range hits {
		if hit.Record.Kind == core.KindDoc {
			t.Fatalf("default recall surfaced a doc record: %#v", hit.Record)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("default recall hits = %d, want 1 fact", len(hits))
	}
	records, err := store.List(ctx, core.Query{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for _, rec := range records {
		if rec.Kind == core.KindDoc {
			t.Fatalf("default list surfaced a doc record: %#v", rec)
		}
	}

	// Explicitly asking for docs surfaces them, metadata intact.
	docHits, err := store.RecallFTS(ctx, core.Query{Query: "gateway memory module", Kinds: []core.Kind{core.KindDoc}})
	if err != nil {
		t.Fatalf("RecallFTS() doc error = %v", err)
	}
	if len(docHits) != 1 || docHits[0].Record.ID != doc.ID {
		t.Fatalf("doc recall = %#v, want the doc record", docHits)
	}
	meta := docHits[0].Record.Metadata
	if meta["path"] != "docs/architecture.md" || meta["source_hash"] != "sha256:abc" || meta["file_size"] != "42" {
		t.Fatalf("doc metadata did not round-trip: %#v", meta)
	}

	// Metadata survives Update too.
	updated, err := store.Update(ctx, doc.ID, core.Record{
		Scope:    config.ScopeProject,
		Kind:     core.KindDoc,
		Content:  "Gateway memory module notes rewritten.",
		Metadata: map[string]string{"path": "docs/architecture.md", "file_size": "50"},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got, found, err := store.Get(ctx, updated.ID)
	if err != nil || !found {
		t.Fatalf("Get() after update: found=%v err=%v", found, err)
	}
	if got.Metadata["file_size"] != "50" {
		t.Fatalf("updated metadata = %#v, want file_size 50", got.Metadata)
	}
}

// Doc records dedupe on (path, content), not content alone: one record per
// file, even when two files hold byte-identical text, and editing one file to
// match another must not collide with the other's record.
func TestStoreDocRecordsAreIdentifiedByPath(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	const shared = "Identical doc body shared by two files."

	saveDoc := func(path, content string) core.Record {
		t.Helper()
		rec, err := store.Save(ctx, core.Record{
			Scope:    config.ScopeProject,
			Kind:     core.KindDoc,
			Content:  content,
			Metadata: map[string]string{core.MetadataKeyPath: path},
		})
		if err != nil {
			t.Fatalf("Save(%q) error = %v", path, err)
		}
		return rec
	}

	first := saveDoc("docs/a.md", shared)
	second := saveDoc("docs/b.md", shared)
	if first.ID == second.ID {
		t.Fatalf("identical docs at different paths collapsed into one record: %q", first.ID)
	}

	// Same path, same content is still one record (idempotent re-sync).
	again := saveDoc("docs/a.md", shared)
	if again.ID != first.ID {
		t.Fatalf("re-saving the same doc created a new record: %q vs %q", again.ID, first.ID)
	}

	// Editing a third file to equal an existing one updates in place.
	third := saveDoc("docs/c.md", "Distinct body.")
	updated, err := store.Update(ctx, third.ID, core.Record{
		Scope:    config.ScopeProject,
		Kind:     core.KindDoc,
		Content:  shared,
		Metadata: map[string]string{core.MetadataKeyPath: "docs/c.md"},
	})
	if err != nil {
		t.Fatalf("Update() to duplicate content error = %v", err)
	}
	if updated.ID != third.ID {
		t.Fatalf("update changed the record id: %q vs %q", updated.ID, third.ID)
	}

	docs, err := store.List(ctx, core.Query{Kinds: []core.Kind{core.KindDoc}})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("doc records = %d, want 3: %#v", len(docs), docs)
	}

	// Non-doc kinds keep content-only dedupe.
	factA, err := store.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "One fact."})
	if err != nil {
		t.Fatalf("Save() fact error = %v", err)
	}
	factB, err := store.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Content: "One fact."})
	if err != nil {
		t.Fatalf("Save() duplicate fact error = %v", err)
	}
	if factA.ID != factB.ID {
		t.Fatalf("duplicate fact created a second record: %q vs %q", factB.ID, factA.ID)
	}
}

// The metadata filter is an isolation boundary (it keeps one workspace's doc
// records out of another's results), so a key it cannot address safely must
// match nothing rather than silently drop the predicate and run unscoped.
func TestStoreMetadataFilterFailsClosedOnUnsafeKey(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	for _, ws := range []string{"/ws/alpha", "/ws/beta"} {
		if _, err := store.Save(ctx, core.Record{
			Scope:   config.ScopeProject,
			Kind:    core.KindDoc,
			Content: "gateway notes for " + ws,
			Metadata: map[string]string{
				core.MetadataKeyWorkspace: ws,
				core.MetadataKeyPath:      "README.md",
			},
		}); err != nil {
			t.Fatalf("Save(%s) error = %v", ws, err)
		}
	}

	// A well-formed key scopes to exactly one workspace.
	scoped, err := store.List(ctx, core.Query{
		Kinds:    []core.Kind{core.KindDoc},
		Metadata: map[string]string{core.MetadataKeyWorkspace: "/ws/alpha"},
	})
	if err != nil {
		t.Fatalf("List() scoped error = %v", err)
	}
	if len(scoped) != 1 || scoped[0].Metadata[core.MetadataKeyWorkspace] != "/ws/alpha" {
		t.Fatalf("scoped list = %#v, want only /ws/alpha", scoped)
	}

	// An unsafe key must yield nothing — never every workspace's records.
	for _, key := range []string{"work'space", "$.workspace", "a b", ""} {
		got, err := store.List(ctx, core.Query{
			Kinds:    []core.Kind{core.KindDoc},
			Metadata: map[string]string{key: "/ws/alpha"},
		})
		if err != nil {
			t.Fatalf("List() with key %q error = %v", key, err)
		}
		if len(got) != 0 {
			t.Fatalf("unsafe metadata key %q returned %d records; the filter failed OPEN", key, len(got))
		}
	}

	// Recall paths must fail closed too, not just List.
	hits, err := store.RecallFTS(ctx, core.Query{
		Query:    "gateway",
		Kinds:    []core.Kind{core.KindDoc},
		Metadata: map[string]string{"bad key": "/ws/alpha"},
	})
	if err != nil {
		t.Fatalf("RecallFTS() error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("unsafe metadata key returned %d recall hits; the filter failed OPEN", len(hits))
	}
}

// UpdateMetadata writes metadata and nothing else, so an empty payload is a
// caller bug rather than a request to clear the column. Clearing would orphan a
// doc record: its workspace/path identity lives in metadata while content_hash
// still encodes them, so the next sync would insert a duplicate instead of
// finding the row.
func TestStoreUpdateMetadataRejectsEmptyMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	meta := map[string]string{
		core.MetadataKeyWorkspace: "/ws/alpha",
		core.MetadataKeyPath:      "README.md",
		"file_size":               "42",
	}
	doc, err := store.Save(ctx, core.Record{
		Scope:    config.ScopeProject,
		Kind:     core.KindDoc,
		Content:  "gateway notes",
		Metadata: meta,
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	for name, empty := range map[string]map[string]string{
		"nil":   nil,
		"empty": {},
	} {
		if err := store.UpdateMetadata(ctx, doc.ID, empty); err == nil {
			t.Fatalf("UpdateMetadata(%s) returned no error; empty metadata must be rejected", name)
		}
		got, found, err := store.Get(ctx, doc.ID)
		if err != nil || !found {
			t.Fatalf("Get() after %s: found=%v err=%v", name, found, err)
		}
		if got.Metadata[core.MetadataKeyWorkspace] != "/ws/alpha" || got.Metadata[core.MetadataKeyPath] != "README.md" {
			t.Fatalf("%s metadata wiped the record's identity: %#v", name, got.Metadata)
		}
	}

	// A populated payload still replaces the stored metadata.
	if err := store.UpdateMetadata(ctx, doc.ID, map[string]string{
		core.MetadataKeyWorkspace: "/ws/alpha",
		core.MetadataKeyPath:      "README.md",
		"file_size":               "99",
	}); err != nil {
		t.Fatalf("UpdateMetadata() error = %v", err)
	}
	got, found, err := store.Get(ctx, doc.ID)
	if err != nil || !found {
		t.Fatalf("Get() after update: found=%v err=%v", found, err)
	}
	if got.Metadata["file_size"] != "99" {
		t.Fatalf("metadata = %#v, want file_size 99", got.Metadata)
	}

	// An unknown id is still reported, not silently ignored.
	if err := store.UpdateMetadata(ctx, "no-such-id", meta); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("UpdateMetadata(unknown id) error = %v, want ErrRecordNotFound", err)
	}
}
