//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/support/testutil/clitest"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

func TestR11a_ConcurrentColdStarts(t *testing.T) {
	for round := 1; round <= 3; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			providerA := providertest.NewScripted(t, providertest.ReplyStep("cold-a"))
			defer providerA.Close()
			providerB := providertest.NewScripted(t, providertest.ReplyStep("cold-b"))
			defer providerB.Close()
			home := newHome(t, providerA.URL())

			results := runConcurrentExec(t, home,
				concurrentExec{providerA.URL(), "cold start a"},
				concurrentExec{providerB.URL(), "cold start b"},
			)
			for i, result := range results {
				if result.Err != nil || result.TimedOut {
					t.Fatalf("process %d: exit=%d timeout=%v err=%v stderr=%s", i, result.ExitCode, result.TimedOut, result.Err, result.Stderr)
				}
			}
			if ok, detail := sqliteIntegrity(t, filepath.Join(home.state.MemoryDir, "memory.db")); !ok {
				t.Fatalf("integrity_check = %s", detail)
			}
			providerA.AssertClean(t)
			providerA.AssertExhausted(t)
			providerB.AssertClean(t)
			providerB.AssertExhausted(t)
		})
	}
}

func TestR11b_ConcurrentMemoryWrites(t *testing.T) {
	seedProvider := providertest.NewScripted(t, providertest.ReplyStep("initialized"))
	defer seedProvider.Close()
	home := newHome(t, seedProvider.URL())
	if seed := runExec(t, home, seedProvider.URL(), "initialize home"); seed.Err != nil {
		t.Fatalf("initialize: %v %s", seed.Err, seed.Stderr)
	}
	beforeSessions := countSessions(t, home)

	providerA := providertest.NewScripted(t,
		managedToolStep("r11b-a", "memory", map[string]any{"action": "save", "content": "r11-proc-a-fact"}),
		providertest.ReplyStep("saved a"),
	)
	defer providerA.Close()
	providerB := providertest.NewScripted(t,
		managedToolStep("r11b-b", "memory", map[string]any{"action": "save", "content": "r11-proc-b-fact"}),
		providertest.ReplyStep("saved b"),
	)
	defer providerB.Close()

	results := runConcurrentExec(t, home,
		concurrentExec{providerA.URL(), "save fact a"},
		concurrentExec{providerB.URL(), "save fact b"},
	)
	for i, result := range results {
		if result.Err != nil || result.TimedOut {
			t.Fatalf("process %d: exit=%d timeout=%v err=%v stderr=%s", i, result.ExitCode, result.TimedOut, result.Err, result.Stderr)
		}
	}
	seen := map[string]int{}
	for _, row := range memoryRows(t, home) {
		seen[row.Content]++
	}
	for _, fact := range []string{"r11-proc-a-fact", "r11-proc-b-fact"} {
		if seen[fact] != 1 {
			t.Fatalf("fact %q count = %d, rows=%#v", fact, seen[fact], memoryRows(t, home))
		}
	}
	if got := countSessions(t, home); got != beforeSessions+2 {
		t.Fatalf("session count = %d, want %d", got, beforeSessions+2)
	}
	if ok, detail := sqliteIntegrity(t, filepath.Join(home.state.MemoryDir, "memory.db")); !ok {
		t.Fatalf("integrity_check = %s", detail)
	}
	providerA.AssertClean(t)
	providerA.AssertExhausted(t)
	providerB.AssertClean(t)
	providerB.AssertExhausted(t)
}

type concurrentExec struct {
	providerURL string
	prompt      string
}

func runConcurrentExec(t *testing.T, home testHome, runs ...concurrentExec) []clitest.Result {
	t.Helper()
	// Force the one-time binary build onto this goroutine: clitest.Run calls
	// t.Fatalf on build failure, which must happen on the test's own
	// goroutine, not the background ones spawned below.
	clitest.Binary(t)
	results := make([]clitest.Result, len(runs))
	var wg sync.WaitGroup
	for i, run := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = clitest.Run(t, clitest.Options{
				State:       home.state,
				CommandArgs: []string{"--base-url=" + run.providerURL, "exec", "--json", run.prompt},
				Timeout:     30 * time.Second,
			})
		}()
	}
	wg.Wait()
	return results
}
