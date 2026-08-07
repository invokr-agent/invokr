package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	memorycore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/markdown"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func runtimeConfigFromEnv(t *testing.T) config.AppConfig {
	t.Helper()
	app, err := config.LoadFile(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return app
}

func TestRuntime_ConfigEffectsInProviderRequests(t *testing.T) {
	provider := providertest.NewScripted(t, providertest.ReplyStep("configured"))
	defer provider.Close()

	t.Setenv("INVOKR_BASE_URL", provider.URL())
	t.Setenv("INVOKR_MODEL", "env-model")
	t.Setenv("INVOKR_TEMPERATURE", "0.25")
	t.Setenv("INVOKR_N_PREDICT", "77")
	t.Setenv("INVOKR_NATIVE_TOOL_CALLING", "true")
	t.Setenv("INVOKR_NATIVE_TOOL_REQUESTS_DISABLED", "false")
	t.Setenv("INVOKR_PARALLEL_TOOL_CALLS_DISABLED", "false")
	t.Setenv("INVOKR_MEMORY_DIR", t.TempDir())
	t.Setenv("INVOKR_TOKF_DISABLED", "true")
	// Session titling is a background curator job that completes against this
	// same provider after Send returns, so it races AssertClean below: on a
	// slow enough runner the title call lands first and reads as an
	// "unexpected completion call". This test is about the request the turn
	// itself builds, so titling is switched off rather than scripted for.
	t.Setenv("INVOKR_MEMORY_DISABLE_SESSION_TITLES", "true")

	app := runtimeConfigFromEnv(t)
	app.Agent.SystemPrompt = "You are an agent."
	app.Agent.MaxTurns = 2
	rt := bootstrapRuntimeForTest(t, app)
	defer func() { _ = rt.Cleanup() }()

	resp, err := rt.Services().Session.Send(context.Background(), "configured request")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if resp.Message != "configured" {
		t.Fatalf("message = %q, want configured", resp.Message)
	}
	provider.AssertClean(t)

	requests := provider.Snapshot().CompletionRequests
	if len(requests) != 1 {
		t.Fatalf("completion requests = %d, want 1", len(requests))
	}
	req := requests[0]
	if req.Model != "env-model" {
		t.Fatalf("model = %q, want env-model", req.Model)
	}
	if req.Temperature != 0.25 {
		t.Fatalf("temperature = %v, want 0.25", req.Temperature)
	}
	if req.MaxCompletionTokens != 77 {
		t.Fatalf("max tokens = %d, want 77", req.MaxCompletionTokens)
	}
	if req.ToolChoice != "auto" {
		t.Fatalf("tool_choice = %#v, want auto", req.ToolChoice)
	}
	if !req.ParallelToolCalls {
		t.Fatal("parallel_tool_calls = false, want true")
	}
	if !completionToolsContain(req.Tools, "tool_search") {
		t.Fatalf("request tools do not contain tool_search: %#v", req.Tools)
	}
}

func TestRuntime_ConfigDisablesNativeToolDeclarations(t *testing.T) {
	provider := providertest.NewScripted(t, providertest.ReplyStep("no tools"))
	defer provider.Close()

	t.Setenv("INVOKR_BASE_URL", provider.URL())
	t.Setenv("INVOKR_NATIVE_TOOL_CALLING", "true")
	t.Setenv("INVOKR_NATIVE_TOOL_REQUESTS_DISABLED", "true")
	t.Setenv("INVOKR_MEMORY_DIR", t.TempDir())
	t.Setenv("INVOKR_TOKF_DISABLED", "true")

	app := runtimeConfigFromEnv(t)
	app.Agent.SystemPrompt = "You are an agent."
	rt := bootstrapRuntimeForTest(t, app)
	defer func() { _ = rt.Cleanup() }()
	if _, err := rt.Services().Session.Send(context.Background(), "no native tools"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	req := provider.Snapshot().CompletionRequests[0]
	if completionToolsContain(req.Tools, "tool_search") {
		t.Fatalf("request tools contain tool_search despite disabled native requests: %#v", req.Tools)
	}
	if req.ParallelToolCalls {
		t.Fatal("parallel_tool_calls = true, want false without tool declarations")
	}
}

func TestRuntime_ConfigParallelToolCallsDisabled(t *testing.T) {
	provider := providertest.NewScripted(t, providertest.ReplyStep("serial"))
	defer provider.Close()

	t.Setenv("INVOKR_BASE_URL", provider.URL())
	t.Setenv("INVOKR_NATIVE_TOOL_CALLING", "true")
	t.Setenv("INVOKR_PARALLEL_TOOL_CALLS_DISABLED", "true")
	t.Setenv("INVOKR_MEMORY_DIR", t.TempDir())
	t.Setenv("INVOKR_TOKF_DISABLED", "true")

	app := runtimeConfigFromEnv(t)
	app.Agent.SystemPrompt = "You are an agent."
	rt := bootstrapRuntimeForTest(t, app)
	defer func() { _ = rt.Cleanup() }()
	if _, err := rt.Services().Session.Send(context.Background(), "serial tools"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	req := provider.Snapshot().CompletionRequests[0]
	if req.ParallelToolCalls {
		t.Fatal("parallel_tool_calls = true, want false")
	}
}

func TestRuntime_ConfigMemoryDirEffect(t *testing.T) {
	memoryDir := t.TempDir()
	provider := providertest.New(t, nil)
	defer provider.Close()

	workspaceRoot := t.TempDir()
	t.Setenv("INVOKR_BASE_URL", provider.URL())
	t.Setenv("INVOKR_MEMORY_DIR", memoryDir)
	t.Setenv("INVOKR_WORKSPACE_ROOT", workspaceRoot)
	t.Setenv("INVOKR_TOKF_DISABLED", "true")

	rt := bootstrapRuntimeForTest(t, runtimeConfigFromEnv(t))
	defer func() { _ = rt.Cleanup() }()
	if _, err := rt.Services().Memory.Save(context.Background(), memorycore.Record{
		Scope:   config.ScopeProject,
		Kind:    memorycore.KindFact,
		Source:  memorycore.SourceMarkdown,
		Content: "config memory dir fact",
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	// Project memory is scoped to the workspace root under the memory dir.
	data, err := os.ReadFile(filepath.Join(markdown.ProjectDirFor(memoryDir, workspaceRoot), "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if !strings.Contains(string(data), "config memory dir fact") {
		t.Fatalf("MEMORY.md missing saved fact: %q", data)
	}
}

func TestRuntime_ConfigToolboxDirEffect(t *testing.T) {
	toolboxDir := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, toolboxDir, "fixture_probe", "Read fixture", map[string]string{
		"main.go": `package main
import "fmt"
func main() { fmt.Print("{\"result\":\"fixture ok\"}") }`,
	})
	provider := providertest.NewScripted(t,
		providertest.ToolCallStep("tool_search", `{"query":"fixture_probe","include_schema":true}`),
		providertest.ReplyStep("found fixture"),
	)
	defer provider.Close()

	t.Setenv("INVOKR_BASE_URL", provider.URL())
	t.Setenv("INVOKR_TOOLBOX_DIR", toolboxDir)
	t.Setenv("INVOKR_MEMORY_DIR", t.TempDir())
	t.Setenv("INVOKR_TOKF_DISABLED", "true")

	app := runtimeConfigFromEnv(t)
	app.Agent.SystemPrompt = "You are an agent."
	app.Agent.MaxTurns = 3
	app.Agent.NativeToolCalling = true
	rt := bootstrapRuntimeForTest(t, app)
	defer func() { _ = rt.Cleanup() }()
	if _, err := rt.Services().Session.Send(context.Background(), "find fixture tool"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !requestsContain(provider.Snapshot().CompletionRequests, "fixture_probe") {
		t.Fatalf("provider requests did not contain fixture_probe: %#v", provider.Snapshot().CompletionRequests)
	}
}

func completionToolsContain(tools any, name string) bool {
	b, _ := json.Marshal(tools)
	return strings.Contains(string(b), name)
}
