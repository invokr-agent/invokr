//go:build e2e

package e2e

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
	systmux "github.com/invokr-ai/invokr/tools/system/tmux"
)

// TestC09_TmuxToolLifecycle exercises the tmux tool's full lifecycle in-process:
// open → read → list → kill → deny open (×2 for denial-cache check) → read
// missing session. Session naming is random (m-<6hex>), so two runInProc phases
// are used: phase 1 opens and records the name from the provider snapshot;
// phase 2 scripts the remaining steps with the real name (mirrors C11's
// read-back-from-state pattern for values the scripted model can't observe).
func TestC09_TmuxToolLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("BLOCKED: tmux not installed")
	}
	// Phase 1: open a session; the scripted model then replies.
	prov1 := providertest.NewScripted(t,
		managedToolStep("c09-open", "tmux", map[string]any{"action": "open", "command": "echo tmux-canary-58"}),
		providertest.ReplyStep("opened"),
	)
	defer prov1.Close()
	home := newHome(t, prov1.URL())
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", systmux.SocketPath(home.workspace), "kill-server").Run()
	})

	r1 := runInProc(t, home, prov1.URL(), "open a tmux session", grantAll)
	if r1.Resp.ToolCallCount != 2 {
		t.Fatalf("phase 1: tool_call_count = %d, want 2", r1.Resp.ToolCallCount)
	}
	if len(r1.Approvals) != 1 {
		t.Fatalf("phase 1: want 1 approval for open, got %d", len(r1.Approvals))
	}
	if len(r1.failureKinds()) != 0 {
		t.Fatalf("phase 1: open should succeed; failures=%#v", r1.failureKinds())
	}
	prov1.AssertClean(t)

	// open returns {"session":"m-xxxxxx"} as its result, fed into the next
	// request — extract the actual name to drive phase 2.
	sess := extractTmuxSession(t, prov1.Snapshot().CompletionRequests)

	// Phase 2: full lifecycle with the real session name.
	prov2 := providertest.NewScripted(t,
		managedToolStep("c09-read", "tmux", map[string]any{"action": "read", "session": sess, "wait_ms": 300}),
		managedToolStep("c09-list", "tmux", map[string]any{"action": "list"}),
		managedToolStep("c09-kill", "tmux", map[string]any{"action": "kill", "session": sess}),
		managedToolStep("c09-open-cat", "tmux", map[string]any{"action": "open", "command": "cat"}),
		managedToolStep("c09-open-cat2", "tmux", map[string]any{"action": "open", "command": "cat"}),
		managedToolStep("c09-miss", "tmux", map[string]any{"action": "read", "session": "definitely-missing"}),
		providertest.ReplyStep("done"),
	)
	defer prov2.Close()

	r2 := runInProc(t, home, prov2.URL(), "exercise tmux lifecycle", denyAll)
	if r2.Resp.ToolCallCount != 12 {
		t.Fatalf("phase 2: tool_call_count = %d, want 12", r2.Resp.ToolCallCount)
	}
	// Exactly one approval: the first open-cat. The identical retry is
	// suppressed by the request-local denial cache.
	if len(r2.Approvals) != 1 {
		t.Fatalf("phase 2: want 1 approval (first open-cat), got %d: %#v", len(r2.Approvals), r2.Approvals)
	}

	snap2 := prov2.Snapshot()
	if result := tmuxResultByID(t, snap2.CompletionRequests, "c09-read"); !strings.Contains(result, "tmux-canary-58") {
		t.Fatalf("read result missing canary: %q", result)
	}
	if result := tmuxResultByID(t, snap2.CompletionRequests, "c09-list"); !strings.Contains(result, "dead=1") {
		t.Fatalf("list result missing dead=1: %q", result)
	}

	// Both open-cat calls are denied; the missing-session read is
	// completed-but-flagged (ToolError.Payload non-empty) → not in ToolFailures.
	var denied, errs int
	for _, f := range r2.failureKinds() {
		switch f.Kind {
		case "denied":
			denied++
		case "error":
			errs++
		}
	}
	if denied != 2 || errs != 0 {
		t.Fatalf("want 2 denied / 0 errors; denied=%d errors=%d; failures=%#v", denied, errs, r2.failureKinds())
	}
	var missingCodes []string
	for _, event := range r2.Tools {
		if event.Name == "tmux" && event.Code != "" && event.Code != "denied" {
			missingCodes = append(missingCodes, event.Code)
		}
	}
	if len(missingCodes) != 1 || missingCodes[0] != "exit 1" {
		t.Fatalf("missing-session codes = %#v, want [exit 1]", missingCodes)
	}

	// Zero managed m-* sessions remain after kill.
	// ponytail: a direct tmux ls is cheaper than a third runInProc phase.
	out, _ := exec.Command("tmux", "-S", systmux.SocketPath(home.workspace), "ls").CombinedOutput()
	if strings.Contains(string(out), "m-") {
		t.Fatalf("managed m-* sessions remain after kill: %s", out)
	}
	prov2.AssertClean(t)
}

func tmuxResultByID(t *testing.T, reqs []providertest.CompletionRequest, id string) string {
	t.Helper()
	for _, req := range reqs {
		for _, msg := range req.Messages {
			if msg.Role == "tool" && msg.ToolCallID == id {
				result, ok := msg.Content.(string)
				if !ok {
					t.Fatalf("tool result %s content type = %T", id, msg.Content)
				}
				return result
			}
		}
	}
	t.Fatalf("tool result %s not found", id)
	return ""
}

// extractTmuxSession finds the first m-<6hex> session name in the serialized
// completion requests (the tmux open result the runner fed back to the model).
func extractTmuxSession(t *testing.T, reqs []providertest.CompletionRequest) string {
	t.Helper()
	data, _ := json.Marshal(reqs)
	m := regexp.MustCompile(`m-[0-9a-f]{6}`).Find(data)
	if m == nil {
		t.Fatalf("no tmux session name (m-xxxxxx) found in completion requests")
	}
	return string(m)
}
