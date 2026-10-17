//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestC08_CodeInterpreter(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("BLOCKED: python3 not found in PATH")
	}

	// Four tool calls: print 2+2 (granted), sleep with timeout (granted),
	// artifact write (granted), final call (denied).
	provider := providertest.NewScripted(t,
		managedToolStep("c08-print", "code_interpreter", map[string]any{
			"code": "print(2+2)",
		}),
		managedToolStep("c08-sleep", "code_interpreter", map[string]any{
			"code":       "import time; time.sleep(8)",
			"timeout_ms": 3000,
		}),
		managedToolStep("c08-artifact", "code_interpreter", map[string]any{
			"code": "import os\nwith open(os.path.join(os.environ['INVOKR_CI_ARTIFACT_DIR'], 'output.txt'), 'w') as f:\n    f.write('artifact-canary-31')",
		}),
		managedToolStep("c08-deny", "code_interpreter", map[string]any{
			"code": "print('denied-canary')",
		}),
		providertest.ReplyStep("done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())

	// Grant the first 3 approval requests; deny the 4th.
	var n int
	approve := func(req approval.Request) approval.Decision {
		n++
		if n >= 4 {
			return approval.Decision{Approved: false, Reason: "scripted deny"}
		}
		return approval.Decision{Approved: true}
	}

	ciDir := filepath.Join(home.state.HomeDir, "code_interpreter")
	dirsBefore := countCISessions(t, ciDir)

	got := runInProc(t, home, provider.URL(), "run code interpreter", approve)

	// Four tool calls total.
	if got.Resp.ToolCallCount != 8 {
		t.Fatalf("tool_call_count = %d, want 8", got.Resp.ToolCallCount)
	}

	// Exactly one denied failure for code_interpreter.
	var denied int
	for _, f := range got.failureKinds() {
		if f.Tool == "code_interpreter" && f.Kind == "denied" {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("denied code_interpreter calls = %d, want 1; failures=%#v", denied, got.failureKinds())
	}

	// Each call raises exactly one approval request (4 total).
	if len(got.Approvals) != 4 {
		t.Fatalf("approval requests = %d, want 8; approvals=%#v", len(got.Approvals), got.Approvals)
	}

	snapshot := provider.Snapshot()
	printResult := codeResultByID(t, snapshot.CompletionRequests, "c08-print")
	if printResult["stdout"] != "4" {
		t.Fatalf("print stdout = %#v, want %q", printResult["stdout"], "4")
	}
	timeoutResult := codeResultByID(t, snapshot.CompletionRequests, "c08-sleep")
	if timeoutResult["timed_out"] != true || timeoutResult["exit_code"] != float64(-1) {
		t.Fatalf("timeout result = %#v, want timed_out=true exit_code=-1", timeoutResult)
	}
	artifactResult := codeResultByID(t, snapshot.CompletionRequests, "c08-artifact")
	artifacts, ok := artifactResult["artifacts"].([]any)
	if !ok || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, want one artifact", artifactResult["artifacts"])
	}
	artifact, ok := artifacts[0].(map[string]any)
	if !ok || artifact["path"] != "session://artifacts/output.txt" || artifact["preview"] != "artifact-canary-31" {
		t.Fatalf("artifact = %#v, want output.txt with canary preview", artifacts[0])
	}

	// Artifact file exists physically under $INVOKR_HOME/code_interpreter/.
	found := false
	_ = filepath.WalkDir(ciDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "artifact-canary-31") {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatalf("artifact-canary-31 not found in any file under %s", ciDir)
	}

	// Denied call created no new session directory.
	// The 3 approved calls each have a unique code hash → 3 session dirs.
	// dirsBefore is 0; after the full run it must still be 3 (not 4).
	_ = dirsBefore
	dirsAfter := countCISessions(t, ciDir)
	if dirsAfter != 3 {
		t.Fatalf("session dirs after run = %d (before=%d), want 3 (denied call must not create a dir)",
			dirsAfter, dirsBefore)
	}

	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

func codeResultByID(t *testing.T, reqs []providertest.CompletionRequest, id string) map[string]any {
	t.Helper()
	for _, req := range reqs {
		for _, msg := range req.Messages {
			if msg.Role != "tool" || msg.ToolCallID != id {
				continue
			}
			text, ok := msg.Content.(string)
			if !ok {
				t.Fatalf("tool result %s content type = %T", id, msg.Content)
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(text), &result); err != nil {
				t.Fatalf("decode tool result %s (%s): %v", id, fmt.Sprint(msg.Content), err)
			}
			return result
		}
	}
	t.Fatalf("tool result %s not found", id)
	return nil
}

// countCISessions counts immediate subdirectories under dir; each represents
// one code_interpreter session keyed by a code-hash prefix.
func countCISessions(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read code_interpreter dir %s: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}
