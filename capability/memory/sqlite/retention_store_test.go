package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/config"

	_ "modernc.org/sqlite"
)

func saveRuntimeRecord(t *testing.T, store *Memory, content string) core.Record {
	t.Helper()
	rec, err := store.Save(context.Background(), core.Record{
		Scope:   config.ScopeProject,
		Kind:    core.KindFact,
		Source:  core.SourceRuntime,
		Content: content,
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return rec
}

func TestTouchRecallReinforces(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rec := saveRuntimeRecord(t, store, "reinforced fact")

	now := time.Now().UTC()
	if err := store.TouchRecall(ctx, []string{rec.ID}, now, 1.5, 10.0); err != nil {
		t.Fatalf("TouchRecall() error = %v", err)
	}
	cands, err := store.SweepCandidates(ctx)
	if err != nil {
		t.Fatalf("SweepCandidates() error = %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("SweepCandidates len = %d, want 1", len(cands))
	}
	c := cands[0]
	if c.RecallCount != 1 {
		t.Fatalf("recall_count = %d, want 1", c.RecallCount)
	}
	if c.Strength != 1.5 {
		t.Fatalf("strength = %v, want 1.5", c.Strength)
	}
	if c.LastRecalledAt.IsZero() {
		t.Fatalf("last_recalled_at not set")
	}
}

func TestTouchRecallCapsStrength(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rec := saveRuntimeRecord(t, store, "capped fact")
	now := time.Now().UTC()
	for i := 0; i < 20; i++ {
		if err := store.TouchRecall(ctx, []string{rec.ID}, now, 1.5, 2.0); err != nil {
			t.Fatalf("TouchRecall() error = %v", err)
		}
	}
	cands, _ := store.SweepCandidates(ctx)
	if cands[0].Strength != 2.0 {
		t.Fatalf("strength = %v, want capped at 2.0", cands[0].Strength)
	}
}

func TestArchiveHidesFromRecallAndRestoreBringsBack(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rec := saveRuntimeRecord(t, store, "archivable database fact")

	if err := store.Archive(ctx, []string{rec.ID}); err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	// Excluded from live recall / FTS.
	hits, err := store.RecallFTS(ctx, core.Query{Query: "database"})
	if err != nil {
		t.Fatalf("RecallFTS() error = %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("archived record still recallable: %#v", hits)
	}
	// Not a sweep candidate anymore (only active records are).
	cands, _ := store.SweepCandidates(ctx)
	if len(cands) != 0 {
		t.Fatalf("archived record still a sweep candidate: %#v", cands)
	}
	// Reachable via archive search only.
	found, err := store.SearchArchive(ctx, "database", 10)
	if err != nil {
		t.Fatalf("SearchArchive() error = %v", err)
	}
	if len(found) != 1 || found[0].ID != rec.ID {
		t.Fatalf("SearchArchive() = %#v, want the archived record", found)
	}

	if err := store.Restore(ctx, []string{rec.ID}); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	hits, err = store.RecallFTS(ctx, core.Query{Query: "database"})
	if err != nil {
		t.Fatalf("RecallFTS() after restore error = %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("restored record not recallable: %#v", hits)
	}
	cands, _ = store.SweepCandidates(ctx)
	if len(cands) != 1 || cands[0].Strength != 1.0 {
		t.Fatalf("restore should reset strength to 1.0: %#v", cands)
	}
	if got, err := store.SearchArchive(ctx, "database", 10); err != nil || len(got) != 0 {
		t.Fatalf("restored record still in archive: %#v (err %v)", got, err)
	}
}

func TestPurgeArchivedBeforeRemovesRecordAndVector(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rec := saveRuntimeRecord(t, store, "purgeable fact")
	if err := store.SaveVector(ctx, rec.ID, []float32{0.1, 0.2, 0.3}); err != nil {
		t.Fatalf("SaveVector() error = %v", err)
	}
	if err := store.Archive(ctx, []string{rec.ID}); err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	// Cutoff in the future removes the just-archived row.
	n, err := store.PurgeArchivedBefore(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("PurgeArchivedBefore() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("purged = %d, want 1", n)
	}
	if got, _ := store.SearchArchive(ctx, "purgeable", 10); len(got) != 0 {
		t.Fatalf("record survived purge: %#v", got)
	}
	var vecCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memory_vectors WHERE record_uuid = ?", rec.ID).Scan(&vecCount); err != nil {
		t.Fatalf("count vectors error = %v", err)
	}
	if vecCount != 0 {
		t.Fatalf("vector survived purge: %d", vecCount)
	}
}

func TestPruneMemoryEventsDropsOldTurns(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{
		SessionID: "s1", Role: "user", Content: "ancient turn", CreatedAt: old,
	}); err != nil {
		t.Fatalf("AppendMemoryEvent() error = %v", err)
	}
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{
		SessionID: "s1", Role: "user", Content: "recent turn", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendMemoryEvent() error = %v", err)
	}
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	n, err := store.PruneMemoryEvents(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneMemoryEvents() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	count, _ := store.CountMemoryEvents(ctx, "s1")
	if count != 1 {
		t.Fatalf("remaining events = %d, want 1", count)
	}
}
