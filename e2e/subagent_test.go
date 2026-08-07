//go:build e2e

package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// writeReviewerDef installs a custom read-only "reviewer" agent definition under
// the home's agents dir and points the config at it. The instructions body and
// the tool allowlist are the observable fingerprints the tests assert on.
func writeReviewerDef(t *testing.T, home *testHome) {
	t.Helper()
	dir := filepath.Join(home.state.HomeDir, "agents")
	home.app.Agents.Dir = dir
	mustWrite(t, filepath.Join(dir, "reviewer.md"), strings.Join([]string{
		"---",
		"name: reviewer",
		"description: Read-only reviewer subagent.",
		"tools:",
		"  - read",
		"---",
		"SUBAGENT-INSTRUCTIONS-CANARY: only read files, never write.",
	}, "\n")+"\n")
}

// TestCustomSubagentDispatch proves that delegate_task with agent:"reviewer"
// actually loads the ~/.invokr/agents/reviewer.md definition: the subagent runs
// with the definition's instructions injected into its system prompt and its tool
// set filtered to the allowlist, and its final text is surfaced to the parent.
func TestCustomSubagentDispatch(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("sub-delegate", "delegate_task", map[string]any{
			"tasks":        []map[string]any{{"label": "rev", "prompt": "review the readme", "agent": "reviewer"}},
			"max_parallel": 1,
		}),
		providertest.ReplyStep("SUBAGENT-REPLY-CANARY reviewed"),
		providertest.ReplyStep("delegation complete"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	writeReviewerDef(t, &home)

	got := runInProc(t, home, provider.URL(), "delegate a review", grantAll)

	if got.Resp.Message != "delegation complete" {
		t.Fatalf("parent final message = %q, want %q", got.Resp.Message, "delegation complete")
	}
	if f := got.failureKinds(); len(f) != 0 {
		t.Fatalf("delegation should succeed, got failures %#v", f)
	}

	reqs := provider.Snapshot().CompletionRequests
	sub := subagentRequest(t, reqs)
	// The definition's instructions were injected into the subagent's prompt.
	if !strings.Contains(marshal(t, sub.Messages), "SUBAGENT-INSTRUCTIONS-CANARY") {
		t.Fatalf("subagent system prompt missing custom instructions:\n%s", marshal(t, sub.Messages))
	}
	// The explicit allowlist filtered the subagent's tool set to read only.
	tools := marshal(t, sub.Tools)
	if !strings.Contains(tools, `"read"`) {
		t.Fatalf("subagent tools missing allowlisted read: %s", tools)
	}
	for _, forbidden := range []string{`"delegate_task"`, `"shell"`, `"apply_patch"`} {
		if strings.Contains(tools, forbidden) {
			t.Fatalf("subagent tool allowlist leaked %s: %s", forbidden, tools)
		}
	}
	// The subagent's reply was surfaced back to the parent as the tool result.
	if !requestsContain(reqs, "SUBAGENT-REPLY-CANARY") {
		t.Fatalf("subagent final text not surfaced to parent:\n%s", marshal(t, reqs))
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

// TestCustomSubagentUnknownName proves an unknown agent name is rejected before
// any child session runs, with an error naming the available definitions.
func TestCustomSubagentUnknownName(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("sub-ghost", "delegate_task", map[string]any{
			"tasks": []map[string]any{{"label": "x", "prompt": "do a thing", "agent": "ghost"}},
		}),
		providertest.ReplyStep("acknowledged"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	writeReviewerDef(t, &home)

	got := runInProc(t, home, provider.URL(), "delegate to a missing agent", grantAll)

	failures := got.failureKinds()
	if len(failures) != 1 || failures[0].Tool != "delegate_task" || failures[0].Kind != "error" {
		t.Fatalf("want one delegate_task error, got %#v", failures)
	}
	reqs := provider.Snapshot().CompletionRequests
	if !requestsContain(reqs, "unknown agent") || !requestsContain(reqs, "reviewer") {
		t.Fatalf("error should name the unknown agent and list available (reviewer):\n%s", marshal(t, reqs))
	}
	if got.Resp.Message != "acknowledged" {
		t.Fatalf("parent final message = %q, want %q", got.Resp.Message, "acknowledged")
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

// subagentRequest returns the single completion request whose system prompt
// carries the reviewer definition's instruction canary — i.e. the child session's.
func subagentRequest(t *testing.T, reqs []providertest.CompletionRequest) providertest.CompletionRequest {
	t.Helper()
	for _, req := range reqs {
		if strings.Contains(marshal(t, req.Messages), "SUBAGENT-INSTRUCTIONS-CANARY") {
			return req
		}
	}
	t.Fatalf("no subagent request found among %d requests", len(reqs))
	return providertest.CompletionRequest{}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
