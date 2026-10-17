package sqlite

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLockMigrationFile_MutualExclusion pins the blocking-exclusive contract:
// every contender must eventually acquire the lock, and never more than one
// may hold it at a time. A switch to a try-lock would fail the first property.
func TestLockMigrationFile_MutualExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db.migrate.lock")

	const contenders = 16

	var (
		holders  atomic.Int32
		acquired atomic.Int32
		maxSeen  atomic.Int32
		wg       sync.WaitGroup
	)

	wg.Add(contenders)
	for range contenders {
		go func() {
			defer wg.Done()

			unlock, err := lockMigrationFile(path)
			if err != nil {
				t.Errorf("lockMigrationFile: %v", err)
				return
			}
			defer unlock()

			acquired.Add(1)

			n := holders.Add(1)
			for {
				prev := maxSeen.Load()
				if n <= prev || maxSeen.CompareAndSwap(prev, n) {
					break
				}
			}

			// Hold briefly so overlapping holders would be observable.
			time.Sleep(time.Millisecond)

			holders.Add(-1)
		}()
	}
	wg.Wait()

	if got := maxSeen.Load(); got != 1 {
		t.Fatalf("concurrent lock holders = %d, want 1", got)
	}
	if got := acquired.Load(); got != contenders {
		t.Fatalf("goroutines that acquired the lock = %d, want %d", got, contenders)
	}
}
