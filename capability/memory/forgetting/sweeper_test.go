package forgetting

import (
	"context"
	"testing"
	"time"
)

type fakeStore struct {
	candidates []Record
	archived   []string
	purgeCut   time.Time
	pruneCut   time.Time
	purgeN     int
	pruneN     int
}

func (f *fakeStore) SweepCandidates(context.Context) ([]Record, error) {
	return f.candidates, nil
}

func (f *fakeStore) Archive(_ context.Context, ids []string) error {
	f.archived = append(f.archived, ids...)
	return nil
}

func (f *fakeStore) PurgeArchivedBefore(_ context.Context, cutoff time.Time) (int, error) {
	f.purgeCut = cutoff
	return f.purgeN, nil
}

func (f *fakeStore) PruneMemoryEvents(_ context.Context, cutoff time.Time) (int, error) {
	f.pruneCut = cutoff
	return f.pruneN, nil
}

func enabledConfig() Config {
	return Config{
		Enabled:               true,
		HalfLifeDays:          14,
		MinRetention:          0.15,
		ReinforcementFactor:   1.5,
		MaxStrength:           10,
		PurgeAfterDays:        30,
		RawEventRetentionDays: 7,
		ExemptKinds:           []string{"preference", "profile"},
	}
}

func TestSweepDisabledIsNoop(t *testing.T) {
	store := &fakeStore{candidates: []Record{{ID: "a", UpdatedAt: time.Now().Add(-365 * 24 * time.Hour)}}}
	cfg := enabledConfig()
	cfg.Enabled = false
	res, err := NewSweeper(store, cfg).Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if !res.Empty() || len(store.archived) != 0 {
		t.Fatalf("disabled sweep mutated store: %#v / %#v", res, store.archived)
	}
}

func TestSweepArchivesStaleAndKeepsFresh(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{
		candidates: []Record{
			{ID: "stale", Kind: "fact", Strength: 1, UpdatedAt: now.Add(-120 * 24 * time.Hour)},
			{ID: "fresh", Kind: "fact", Strength: 1, UpdatedAt: now.Add(-1 * 24 * time.Hour)},
			{ID: "pinned", Kind: "preference", Strength: 1, UpdatedAt: now.Add(-120 * 24 * time.Hour)},
		},
		purgeN: 2,
		pruneN: 3,
	}
	s := NewSweeper(store, enabledConfig())
	s.now = func() time.Time { return now }
	res, err := s.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(res.Archived) != 1 || res.Archived[0] != "stale" {
		t.Fatalf("archived = %#v, want [stale]", res.Archived)
	}
	if res.Purged != 2 || res.Pruned != 3 {
		t.Fatalf("unexpected purge/prune: %#v", res)
	}
	wantPurgeCut := now.Add(-30 * 24 * time.Hour)
	if !store.purgeCut.Equal(wantPurgeCut) {
		t.Fatalf("purge cutoff = %v, want %v", store.purgeCut, wantPurgeCut)
	}
	wantPruneCut := now.Add(-7 * 24 * time.Hour)
	if !store.pruneCut.Equal(wantPruneCut) {
		t.Fatalf("prune cutoff = %v, want %v", store.pruneCut, wantPruneCut)
	}
}

func TestSweepSkipsPurgeAndPruneWhenDisabled(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStore{purgeN: 5, pruneN: 5}
	cfg := enabledConfig()
	cfg.PurgeAfterDays = 0
	cfg.RawEventRetentionDays = 0
	s := NewSweeper(store, cfg)
	s.now = func() time.Time { return now }
	res, err := s.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if res.Purged != 0 || res.Pruned != 0 {
		t.Fatalf("purge/prune ran while disabled: %#v", res)
	}
	if !store.purgeCut.IsZero() || !store.pruneCut.IsZero() {
		t.Fatalf("store purge/prune called while disabled")
	}
}
