//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/memory/markdown"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

var c11RecordID = regexp.MustCompile(`"id":"([^"]+)"`)

// TestC11_MemoryUpdateMergeAndNoPhantomRecord covers the two regressions from
// issue #299: an update naming only id and content keeps the stored kind, and
// the assistant's own numbered-list reply never lands in MEMORY.md.
func TestC11_MemoryUpdateMergeAndNoPhantomRecord(t *testing.T) {
	update := providertest.NativeToolCallStep("c11-update", "memory", map[string]any{})
	update.MutateForRequest = func(req providertest.CompletionRequest, step *providertest.CompletionStep) {
		id := c11SavedRecordID(req)
		step.ToolCalls[0].Function.Arguments = map[string]any{
			"action": "update", "id": id, "content": "mem-canary-proj-11-v2",
		}
	}
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("c11-save", "memory", map[string]any{
			"action": "save", "scope": "project", "kind": "decision",
			"content": "mem-canary-proj-11",
		}),
		update,
		providertest.NativeToolCallStep("c11-list", "memory", map[string]any{
			"action": "list", "scopes": []string{"project"},
		}),
		// Free-form prose that the old curator harvested into MEMORY.md.
		providertest.ReplyStep("1) instead of the project-scoped record we just updated\n"+
			"2) the project memory file stays small\n"+
			"3) the repository record set stays reviewable"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	rewriteConfig(t, &home, func(app *config.AppConfig) { app.Provider.BaseURL = provider.URL() })

	got := runInProc(t, home, provider.URL(), "List three reasons to keep project memory small", grantAll)

	if len(got.Resp.ToolFailures) != 0 {
		t.Fatalf("tool failures: %#v", got.Resp.ToolFailures)
	}
	if len(got.Approvals) != 0 {
		t.Fatalf("memory writes should not prompt for approval: %#v", got.Approvals)
	}

	updated := tmuxResultByID(t, provider.Snapshot().CompletionRequests, "c11-update")
	if !strings.Contains(updated, `"kind":"decision"`) || !strings.Contains(updated, "mem-canary-proj-11-v2") {
		t.Fatalf("update response = %s, want the merged record keeping kind decision", updated)
	}
	if strings.Contains(updated, "0001-01-01T00:00:00Z") {
		t.Fatalf("update response carries a zero created_at: %s", updated)
	}
	listed := tmuxResultByID(t, provider.Snapshot().CompletionRequests, "c11-list")
	if !strings.Contains(listed, `"kind":"decision"`) || !strings.Contains(listed, "mem-canary-proj-11-v2") {
		t.Fatalf("list = %s, want the merged record on disk", listed)
	}

	// The reply text must not have been curated into project markdown memory.
	projectMD := filepath.Join(markdown.ProjectDirFor(home.state.MemoryDir, home.workspace), "MEMORY.md")
	data, err := os.ReadFile(projectMD)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if strings.Contains(string(data), "instead of the project-scoped record") {
		t.Fatalf("assistant prose materialised in MEMORY.md:\n%s", data)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

// c11SavedRecordID pulls the saved record's id out of the save tool's result so
// the update call addresses the real record.
func c11SavedRecordID(req providertest.CompletionRequest) string {
	for _, msg := range req.Messages {
		if msg.Role != "tool" || msg.ToolCallID != "c11-save" {
			continue
		}
		content, _ := msg.Content.(string)
		if m := c11RecordID.FindStringSubmatch(content); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}
