package forgetting

import (
	"context"
	"time"
)

// Store is the narrow write surface the sweeper needs. The SQLite memory store
// satisfies it; keeping it minimal keeps the sweeper testable with a fake.
type Store interface {
	SweepCandidates(ctx context.Context) ([]Record, error)
	Archive(ctx context.Context, ids []string) error
	PurgeArchivedBefore(ctx context.Context, cutoff time.Time) (int, error)
	PruneMemoryEvents(ctx context.Context, cutoff time.Time) (int, error)
}

// Result reports what a single sweep changed, so callers can emit audit events
// and tests can assert on the outcome without inspecting the store.
type Result struct {
	Archived []string
	Purged   int
	Pruned   int
}

func (r Result) Empty() bool { return len(r.Archived) == 0 && r.Purged == 0 && r.Pruned == 0 }

// Sweeper runs the decay maintenance pass: archive below-threshold records,
// optionally purge old archived rows, and prune old summarized raw events. It is
// schedulerless — callers invoke Sweep at existing lifecycle points.
type Sweeper struct {
	store Store
	cfg   Config
	now   func() time.Time
}

// NewSweeper builds a sweeper over store with cfg. now defaults to time.Now.
func NewSweeper(store Store, cfg Config) *Sweeper {
	return &Sweeper{store: store, cfg: cfg, now: time.Now}
}

// Sweep runs the archive, purge, and prune passes. It is a no-op when
// forgetting is disabled, so callers can invoke it unconditionally.
func (s *Sweeper) Sweep(ctx context.Context) (Result, error) {
	var res Result
	if !s.cfg.Enabled {
		return res, nil
	}
	now := s.now().UTC()
	cands, err := s.store.SweepCandidates(ctx)
	if err != nil {
		return res, err
	}
	var forget []string
	for _, rec := range cands {
		if ShouldForget(rec, now, s.cfg) {
			forget = append(forget, rec.ID)
		}
	}
	if len(forget) > 0 {
		if err := s.store.Archive(ctx, forget); err != nil {
			return res, err
		}
		res.Archived = forget
	}
	if err := s.retentionPass(ctx, now, s.cfg.PurgeAfterDays, s.store.PurgeArchivedBefore, &res.Purged); err != nil {
		return res, err
	}
	if err := s.retentionPass(ctx, now, s.cfg.RawEventRetentionDays, s.store.PruneMemoryEvents, &res.Pruned); err != nil {
		return res, err
	}
	return res, nil
}

// retentionPass deletes everything older than days and records how many rows
// went. A non-positive retention disables the pass, leaving count untouched.
func (s *Sweeper) retentionPass(ctx context.Context, now time.Time, days int, remove func(context.Context, time.Time) (int, error), count *int) error {
	if days <= 0 {
		return nil
	}
	n, err := remove(ctx, now.Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		return err
	}
	*count = n
	return nil
}
