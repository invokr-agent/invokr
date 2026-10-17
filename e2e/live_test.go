//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveC03_ShellSelection(t *testing.T) {
	testLiveToolSelection(t, "C03", "shell", "Run pwd in the project workspace and report the exact path.")
}

func TestLiveC05_ReadFileSelection(t *testing.T) {
	testLiveToolSelection(t, "C05", "read", "Read README.md and tell me the project codename.")
}

func TestLiveC06_RetrievalSelection(t *testing.T) {
	testLiveToolSelection(t, "C06", "retrieval", "Search project documentation for which module owns recall.")
}

func TestLiveC11_MemorySelection(t *testing.T) {
	testLiveToolSelection(t, "C11", "memory", "Save a project fact that the live e2e marker is live-memory-11.")
}

func testLiveToolSelection(t *testing.T, id, tool, prompt string) {
	t.Helper()
	baseURL := requireLive(t)
	home := newHome(t, baseURL)
	home.live = true
	result := runExec(t, home, baseURL, prompt)
	if result.Err != nil {
		t.Fatalf("[%s] exec failed: exit=%d err=%v", id, result.ExitCode, result.Err)
	}
	// Selecting the right tool is the whole point of this tier: a miss
	// is the failure it exists to catch, not an advisory note.
	ledger := parseLedger(t, result.Stdout)
	if !ledgerSawTool(ledger, tool) {
		t.Errorf("[%s] model did not select %s", id, tool)
	}
}

// requireLive gates a test on the live tier: it skips unless INVOKR_E2E_LIVE=1,
// resolves the provider URL, and fails (never skips) if that provider is
// unreachable — a skipped live case must never read as a pass.
func requireLive(t *testing.T) string {
	t.Helper()
	if os.Getenv("INVOKR_E2E_LIVE") != "1" {
		t.Skip("live tier disabled")
	}
	baseURL := strings.TrimRight(os.Getenv("INVOKR_E2E_LIVE_URL"), "/")
	if baseURL == "" {
		baseURL = "http://llamacpp:8080"
	}
	if err := waitForLiveProvider(baseURL, 2*time.Minute); err != nil {
		t.Fatalf("live provider %s unavailable: %v", baseURL, err)
	}
	return baseURL
}

func waitForLiveProvider(baseURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second}
	for ctx.Err() == nil {
		for _, path := range []string{"/health", "/props"} {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("provider %s not ready before %s", baseURL, timeout)
}
