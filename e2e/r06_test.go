//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestR06_SIGKILLMidTurnRestartsCleanly(t *testing.T) {
	seedProvider := providertest.NewScripted(t,
		providertest.ReplyStep("r06-seed-one"),
		providertest.ReplyStep("r06-seed-two"),
	)
	defer seedProvider.Close()
	home := newHome(t, seedProvider.URL())
	var seedIDs []string
	for _, prompt := range []string{"seed one", "seed two"} {
		result := runExec(t, home, seedProvider.URL(), prompt)
		if result.Err != nil {
			t.Fatalf("seed %q: %v %s", prompt, result.Err, result.Stderr)
		}
		id, _ := parseLedger(t, result.Stdout).Completed["thread_id"].(string)
		seedIDs = append(seedIDs, id)
	}
	if got := countSessions(t, home); got != 2 {
		t.Fatalf("seed session count = %d, want 2", got)
	}

	chunkSent := make(chan struct{})
	var once sync.Once
	stallProvider := providertest.NewScripted(t, providertest.CompletionStep{
		SSEChunks:        []providertest.CompletionStreamChunk{{Content: "partial-turn"}},
		StallAfterChunks: 1,
		StallDuration:    time.Minute,
		AfterChunk:       func(int) { once.Do(func() { close(chunkSent) }) },
	})
	defer stallProvider.Close()
	process := clitest.Start(t, clitest.Options{
		State:       home.state,
		CommandArgs: []string{"--base-url=" + stallProvider.URL(), "exec", "--json", "start interrupted turn"},
		Timeout:     30 * time.Second,
		Env:         map[string]string{"INVOKR_TOOLBOX_ALLOW_UNSANDBOXED": "1"},
	})
	select {
	case <-chunkSent:
	case <-time.After(5 * time.Second):
		t.Fatal("provider never sent first chunk")
	}
	if process.PID() <= 0 {
		t.Fatal("started process has no PID")
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("SIGKILL process group: %v", err)
	}
	killed := process.Wait()
	if killed.Err == nil || killed.TimedOut {
		t.Fatalf("killed process: timeout=%v err=%v", killed.TimedOut, killed.Err)
	}
	if got := countSessions(t, home); got != 2 {
		t.Fatalf("partial killed turn persisted: sessions=%d, want 2", got)
	}

	restartProvider := providertest.NewScripted(t,
		managedToolStep("r06-recent", "session_search", map[string]any{"action": "recent", "max_results": 10}),
		providertest.ReplyStep("restart clean"),
	)
	defer restartProvider.Close()
	restarted := runExec(t, home, restartProvider.URL(), "list prior sessions")
	if restarted.Err != nil || restarted.TimedOut {
		t.Fatalf("restart: timeout=%v err=%v stderr=%s", restarted.TimedOut, restarted.Err, restarted.Stderr)
	}
	results := toolResults(restartProvider.Snapshot().CompletionRequests)
	if !containsAll(results, seedIDs...) {
		t.Fatalf("recent sessions missing seeded turns: %s", results)
	}
	entries, err := filepath.Glob(filepath.Join(home.state.MemoryDir, "sessions", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range entries {
		parseJSONL(t, path)
	}
	restartProvider.AssertClean(t)
	restartProvider.AssertExhausted(t)
}

func containsAll(text string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(text, value) {
			return false
		}
	}
	return true
}
