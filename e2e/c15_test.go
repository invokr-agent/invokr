//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestC15_Delegate drives a parent turn that delegates a subtask; the same
// scripted provider serves the child's completion, and the child's result must
// surface in the parent's tool result. Delegation raises exactly one approval.
func TestC15_Delegate(t *testing.T) {
	provider := providertest.NewScripted(t,
		managedToolStep("c15-delegate", "delegate_task", map[string]any{
			"tasks":        []map[string]any{{"label": "math", "prompt": "compute 17*19 without tools"}},
			"max_parallel": 1,
		}),
		providertest.ReplyStep("child-canary-323"), // served to the child agent
		providertest.ReplyStep("parent done"),      // parent's final answer
	)
	defer provider.Close()
	home := newHome(t, provider.URL())

	got := runInProc(t, home, provider.URL(), "delegate the math", grantAll)

	if got.Resp.ToolCallCount != 2 {
		t.Fatalf("tool_call_count = %d, want 2 (enable + delegate_task)", got.Resp.ToolCallCount)
	}
	if len(got.failureKinds()) != 0 {
		t.Fatalf("delegate_task should succeed: %#v", got.failureKinds())
	}
	if len(got.Approvals) != 1 || got.Approvals[0].Tool != "delegate_task" {
		t.Fatalf("want exactly one delegate_task approval, got %#v", got.Approvals)
	}
	// The child's reply is returned specifically as the delegate tool result.
	results := toolResults(provider.Snapshot().CompletionRequests)
	if !strings.Contains(results, `\"label\":\"math\"`) || !strings.Contains(results, `\"final_text\":\"child-canary-323\"`) {
		t.Fatalf("delegate result missing child attribution: %s", results)
	}
	rows := memoryRows(t, home)
	if len(rows) != 1 || rows[0].SessionID != got.Services.Session.SessionID() || rows[0].Scope != "project" ||
		rows[0].Kind != "episode" || !strings.Contains(rows[0].Content, "compute 17*19 without tools") ||
		!strings.Contains(rows[0].Content, "child-canary-323") {
		t.Fatalf("persisted delegate episode = %#v", rows)
	}
}
