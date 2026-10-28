package memory

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/capability/memory/summary"
)

// blockingQueueSummarizer parks every Summarize call until release is closed,
// letting a test fill the curator's queue while the worker is stuck on job 0.
type blockingQueueSummarizer struct {
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingQueueSummarizer) Summarize(context.Context, summary.StructuredSummaryInput) (summary.MemorySummary, error) {
	b.calls.Add(1)
	<-b.release
	return summary.MemorySummary{}, nil
}

// TestCuratorEnqueueDropsWhenFull proves the queue is bounded: once the
// worker is blocked on one job and the buffer (curatorQueueSize) is full,
// further enqueue calls drop the job instead of blocking the caller.
func TestCuratorEnqueueDropsWhenFull(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "s", Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("AppendMemoryEvent() error = %v", err)
	}
	blocking := &blockingQueueSummarizer{release: make(chan struct{})}
	manager := TieredManager{sqlite: store, summarizer: blocking}
	c := newCurator(manager)
	c.startWorker()
	t.Cleanup(func() {
		close(blocking.release)
		_ = c.Close()
	})

	// One job is picked up immediately and blocks the worker on Summarize;
	// the next curatorQueueSize jobs fill the buffered channel exactly.
	for range curatorQueueSize + 1 {
		c.enqueue(summaryJob{sessionID: "s", limit: 1})
	}
	select {
	case <-waitFor(blocking.calls.Load, 1):
	case <-time.After(2 * time.Second):
		t.Fatal("worker never picked up the first job")
	}

	// The queue is now full. One more enqueue must not block this goroutine,
	// proving the drop-when-full policy.
	done := make(chan struct{})
	go func() {
		c.enqueue(summaryJob{sessionID: "s", limit: 1})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked on a full queue instead of dropping")
	}
}

// waitFor polls get until it returns want, signalling on the returned
// channel. It's a small helper to avoid a bare sleep while the worker
// goroutine picks up its first job.
func waitFor(get func() int32, want int32) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for get() < want {
			time.Sleep(time.Millisecond)
		}
		close(done)
	}()
	return done
}

// TestCuratorCloseDrainsQueue proves Close waits for already-queued jobs to
// finish rather than discarding them, and stays idempotent.
func TestCuratorCloseDrainsQueue(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{SessionID: "s", Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("AppendMemoryEvent() error = %v", err)
	}
	var processed atomic.Int32
	manager := TieredManager{sqlite: store, summarizer: countingSummarizer{count: &processed}}
	c := newCurator(manager)
	c.startWorker()

	c.enqueue(summaryJob{sessionID: "s", limit: 1})
	c.enqueue(summaryJob{sessionID: "s", limit: 1})

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := processed.Load(); got != 2 {
		t.Fatalf("processed = %d, want 2 (Close must drain queued jobs)", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

type countingSummarizer struct {
	count *atomic.Int32
}

func (c countingSummarizer) Summarize(context.Context, summary.StructuredSummaryInput) (summary.MemorySummary, error) {
	c.count.Add(1)
	return summary.MemorySummary{}, nil
}
