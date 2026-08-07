//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestR07_TruncatedSessionTailRecoversWithWarning(t *testing.T) {
	seedProvider := providertest.NewScripted(t, providertest.ReplyStep("r07 intact canary"))
	defer seedProvider.Close()
	home := newHome(t, seedProvider.URL())
	seed := runExec(t, home, seedProvider.URL(), "seed session")
	if seed.Err != nil {
		t.Fatalf("seed exec: %v %s", seed.Err, seed.Stderr)
	}
	sessionID, _ := parseLedger(t, seed.Stdout).Completed["thread_id"].(string)
	if sessionID == "" {
		t.Fatal("seed session has no thread_id")
	}
	sessionPath := filepath.Join(home.state.MemoryDir, "sessions", sessionID+".jsonl")
	f, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"role":`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	provider := providertest.NewScripted(t, providertest.ReplyStep("resumed safely"))
	defer provider.Close()
	logPath := filepath.Join(home.state.HomeDir, "r07.log")
	result := clitest.Run(t, clitest.Options{
		State: home.state,
		CommandArgs: []string{
			"--base-url=" + provider.URL(), "--log-level=debug", "--log-output=" + logPath,
			"exec", "--json", "--resume", sessionID, "continue",
		},
		Timeout: 30 * time.Second,
	})
	if result.Err != nil || result.TimedOut {
		t.Fatalf("resume: timeout=%v err=%v stderr=%s", result.TimedOut, result.Err, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "resumed safely") {
		t.Fatalf("resume output missing reply: %s", result.Stdout)
	}
	logText := readFile(t, logPath)
	if !strings.Contains(logText, sessionID) || !strings.Contains(logText, "line") || !strings.Contains(strings.ToLower(logText), "warn") {
		t.Fatalf("warning does not name session and line:\n%s", logText)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
