//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

var d07ProcessID = regexp.MustCompile(`"process_id":"([0-9a-f-]{36})"`)

func TestD07_BackgroundShellProcesses(t *testing.T) {
	var mu sync.Mutex
	var processID string
	dynamic := func(id, action string, extra map[string]any) providertest.CompletionStep {
		step := managedToolStep(id, "process", nil)
		step.MutateForRequest = func(req providertest.CompletionRequest, out *providertest.CompletionStep) {
			mu.Lock()
			defer mu.Unlock()
			for _, message := range req.Messages {
				match := d07ProcessID.FindStringSubmatch(fmt.Sprint(message.Content))
				if len(match) == 2 {
					processID = match[1]
				}
			}
			args := map[string]any{"action": action, "process_id": processID}
			for key, value := range extra {
				args[key] = value
			}
			out.ToolCalls[1].Function.Arguments.(map[string]any)["arguments"] = args
		}
		return step
	}
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("d07-start", "shell", map[string]any{
			"command": "touch d07-marker; echo d07-start; sleep 1; echo d07-end", "background": true,
		}),
		dynamic("d07-list", "list", nil),
		dynamic("d07-poll", "poll", nil),
		dynamic("d07-wait", "wait", map[string]any{"timeout_ms": 3000}),
		dynamic("d07-log", "log", map[string]any{"max_bytes": 1024}),
		dynamic("d07-kill", "kill", nil),
		providertest.ReplyStep("done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	home.app.Shell.AllowedCommands = []string{"touch", "sleep"}
	got := runInProc(t, home, provider.URL(), "run background process", grantAll)
	mu.Lock()
	pid := processID
	mu.Unlock()
	if pid == "" || len(got.Resp.ToolFailures) != 0 || len(got.Approvals) != 1 {
		t.Fatalf("id=%q failures=%#v approvals=%d", pid, got.Resp.ToolFailures, len(got.Approvals))
	}
	logs, err := filepath.Glob(filepath.Join(home.state.HomeDir, "process", "*", pid+".log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("find relocated process log: paths=%v err=%v", logs, err)
	}
	logPath := logs[0]
	data, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(data), "d07-start") != 1 || strings.Count(string(data), "d07-end") != 1 {
		t.Fatalf("process log %q: %v", data, err)
	}
	if strings.HasPrefix(logPath, home.workspace) {
		t.Fatalf("process log polluted workspace: %s", logPath)
	}

	foreign := providertest.NewScripted(t,
		managedToolStep("d07-foreign", "process", map[string]any{"action": "poll", "process_id": pid}),
		managedToolStep("d07-bogus", "process", map[string]any{"action": "kill", "process_id": "00000000-0000-0000-0000-000000000000"}),
		providertest.ReplyStep("done"),
	)
	defer foreign.Close()
	second := runInProc(t, home, foreign.URL(), "access foreign process", grantAll)
	if len(second.Resp.ToolFailures) != 2 {
		t.Fatalf("foreign failures=%#v", second.Resp.ToolFailures)
	}
	results := toolResults(foreign.Snapshot().CompletionRequests)
	if strings.Count(results, "process not found in this session") < 2 {
		t.Fatalf("foreign and bogus ids did not fail identically: %s", results)
	}
}
