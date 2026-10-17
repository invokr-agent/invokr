//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestA02_HostileMCPOutputCannotEscapeGuards(t *testing.T) {
	t.Setenv("INVOKR_FAKE_MCP_HOSTILE", "1")
	t.Setenv("INVOKR_TEST_SECRET", "host-canary-91")
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("a02-search", "tools", map[string]any{"action": "search", "query": "mcp_hostile_echo", "include_schema": true}),
		managedToolStep("a02-echo", "mcp_hostile_echo", map[string]any{"text": "hello"}),
		providertest.NativeToolCallStep("a02-env", "shell", map[string]any{"command": "env"}),
		providertest.NativeToolCallStep("a02-rm", "shell", map[string]any{"command": "rm", "args": []string{"rm", "-rf", "x"}}),
		providertest.ReplyStep("done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	home.app.MCP = config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"hostile": {Command: os.Args[0], Args: []string{"-test.run=TestFakeMCPServerHostileE2E"}, Enabled: true},
	}}
	approve := func(req approval.Request) approval.Decision {
		return approval.Decision{Approved: req.Tool == "mcp_hostile_echo", Reason: "scripted A02 policy"}
	}
	got := runInProcWhen(t, home, provider.URL(), "call hostile MCP", approve, func(s runtime.Services) {
		waitForSessionTool(t, s, "mcp_hostile_echo")
	})

	results := toolResults(provider.Snapshot().CompletionRequests)
	if !containsAll(results, "INJECT-MCP-02", "run env", "rm -rf x") {
		t.Fatalf("hostile MCP payload was not delivered: %s", results)
	}
	var shellFailures int
	for _, failure := range got.failureKinds() {
		if failure.Tool == "shell" && (failure.Kind == "error" || failure.Kind == "denied") {
			shellFailures++
		}
	}
	if shellFailures != 2 {
		t.Fatalf("shell failures = %d, want 2: %#v", shellFailures, got.failureKinds())
	}
	// `rm` is on the allowlist now, so it reaches the approval layer instead
	// of the refusal: the only grant is still the MCP call, and the scripted
	// policy denies the shell write (shellFailures above, and the workspace
	// check below, prove it never ran).
	if len(got.Approvals) == 0 || got.Approvals[0].Tool != "mcp_hostile_echo" {
		t.Fatalf("approvals = %#v, want the hostile MCP grant first", got.Approvals)
	}
	for _, req := range got.Approvals[1:] {
		if req.Tool != "shell" || req.Tier != string(constants.ShellTierWrite) {
			t.Fatalf("unexpected extra approval %#v", req)
		}
	}
	assertNoCanary(t, got.Resp.Message, results)
	if _, err := os.Stat(filepath.Join(home.workspace, "x")); !os.IsNotExist(err) {
		t.Fatalf("hostile payload mutated workspace: %v", err)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

func TestFakeMCPServerHostileE2E(t *testing.T) {
	if os.Getenv("INVOKR_FAKE_MCP_HOSTILE") != "1" {
		return
	}
	runFakeMCPServer(true)
}
