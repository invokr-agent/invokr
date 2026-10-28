//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

func TestD03_MCPLifecycleAndFiltering(t *testing.T) {
	t.Run("enabled tools filter", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			providertest.NativeToolCallStep("d03-search", "tools", map[string]any{"action": "search", "query": "mcp_fixture_echo", "include_schema": true}),
			providertest.ReplyStep("filtered"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		home.app.MCP = config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"fixture": {Command: os.Args[0], Args: []string{"-test.run=TestFakeMCPServerE2E"}, Enabled: true, EnabledTools: []string{"not_echo"}},
		}}
		rewriteConfig(t, &home, func(*config.AppConfig) {})
		result := runExecEnv(t, home, provider.URL(), "find MCP tool", map[string]string{"INVOKR_FAKE_MCP": "1"})
		if result.Err != nil {
			t.Fatalf("filtered run: %v %s", result.Err, result.Stderr)
		}
		if strings.Contains(toolResults(provider.Snapshot().CompletionRequests), "mcp_fixture_echo") {
			t.Fatal("filtered MCP echo surfaced in tool_search result")
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("optional broken server warns once", func(t *testing.T) {
		provider := providertest.NewScripted(t, providertest.ReplyStep("continued"))
		defer provider.Close()
		home := newHome(t, provider.URL())
		logPath := filepath.Join(home.state.HomeDir, "d03-optional.log")
		home.app.Log = config.LoggingConfig{Level: "debug", Output: logPath}
		home.app.MCP = config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"fixture": {Command: "/definitely/missing-mcp", Enabled: true, Required: false},
		}}
		rewriteConfig(t, &home, func(*config.AppConfig) {})
		result := runExec(t, home, provider.URL(), "continue without MCP")
		if result.Err != nil {
			t.Fatalf("optional run: %v %s", result.Err, result.Stderr)
		}
		logText := strings.ToLower(readFile(t, logPath))
		if strings.Count(logText, "skipping optional mcp server") != 1 {
			t.Fatalf("optional MCP warning count != 1:\n%s", logText)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("required broken server exposes error tool", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			providertest.NativeToolCallStep("d03-error", "mcp_fixture_error", map[string]any{}),
			providertest.ReplyStep("handled"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		home.app.MCP = config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"fixture": {Command: "/definitely/missing-mcp", Enabled: true, Required: true},
		}}
		rewriteConfig(t, &home, func(*config.AppConfig) {})
		result := runExec(t, home, provider.URL(), "inspect required MCP failure")
		if result.Err != nil {
			t.Fatalf("required run: %v %s", result.Err, result.Stderr)
		}
		failures := parseLedger(t, result.Stdout).failures()
		if len(failures) != 1 || failures[0].Tool != "mcp_fixture_error" || failures[0].Kind != "error" {
			t.Fatalf("failures = %#v, want mcp_fixture_error", failures)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})
}
