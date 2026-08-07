package tools

import (
	"context"
	"encoding/json"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools/core"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNewCaller_InvokesGoToolboxPlugin(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, root, "echo_value", "Echo value from arguments", map[string]string{
		"main.go": `package main
import (
  "encoding/json"
  "fmt"
  "io"
  "os"
)
type toolCall struct {
  Name string ` + "`json:\"name\"`" + `
  Arguments json.RawMessage ` + "`json:\"arguments\"`" + `
}
type args struct {
  Value string ` + "`json:\"value\"`" + `
}
type toolResult struct {
  Result string ` + "`json:\"result,omitempty\"`" + `
  Error string ` + "`json:\"error,omitempty\"`" + `
}
func main() {
  in, err := io.ReadAll(os.Stdin)
  if err != nil {
    b, _ := json.Marshal(toolResult{Error: err.Error()})
    fmt.Print(string(b))
    return
  }
  var call toolCall
  if err := json.Unmarshal(in, &call); err != nil {
    b, _ := json.Marshal(toolResult{Error: err.Error()})
    fmt.Print(string(b))
    return
  }
  var a args
  _ = json.Unmarshal(call.Arguments, &a)
  b, _ := json.Marshal(toolResult{Result: a.Value})
  fmt.Print(string(b))
}`,
	})

	t.Setenv("INVOKR_TOOLBOX_DIR", root)

	caller := NewCaller(
		core.CallerDeps{App: testAppConfigFromEnv(t)},
		zap.NewNop(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	output, err := caller.HandleCall(ctx, &core.ToolCall{
		Name:      "echo_value",
		Arguments: json.RawMessage(`{"value":"hello"}`),
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}
	if output != "hello" {
		t.Fatalf("expected plugin output hello, got %q", output)
	}
}

func TestNewCaller_InvokesPythonToolboxPluginInPerToolVenv(t *testing.T) {
	root := t.TempDir()
	pluginDir := testutil.MustWriteToolboxPlugin(t, root, "python_env", "Report Python environment", map[string]string{
		"main.py": `import json
import os
import sys

print(json.dumps({
    "result": json.dumps({
        "executable": sys.executable,
        "prefix": sys.prefix,
        "virtual_env": os.environ.get("VIRTUAL_ENV", "")
    })
}), end="")
`,
	})
	testutil.MustWriteFile(t, filepath.Join(pluginDir, "requirements.txt"), ``)

	t.Setenv("INVOKR_TOOLBOX_DIR", root)

	caller := NewCaller(
		core.CallerDeps{App: testAppConfigFromEnv(t)},
		zap.NewNop(),
	)
	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "python_env",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}

	var got struct {
		Executable string `json:"executable"`
		Prefix     string `json:"prefix"`
		VirtualEnv string `json:"virtual_env"`
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("parse plugin output %q: %v", output, err)
	}

	wantVenv := filepath.Join(pluginDir, ".venv")
	if got.VirtualEnv != wantVenv {
		t.Fatalf("expected VIRTUAL_ENV %q, got %q", wantVenv, got.VirtualEnv)
	}
	if got.Prefix != wantVenv {
		t.Fatalf("expected sys.prefix %q, got %q", wantVenv, got.Prefix)
	}
	if filepath.Dir(got.Executable) != filepath.Join(wantVenv, "bin") {
		t.Fatalf("expected executable under the tool's own venv, got %q", got.Executable)
	}
}

func TestNewCaller_InvokesPythonToolboxPluginWithoutRequirementsDirectly(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, root, "python_no_requirements", "Report direct Python execution", map[string]string{
		"main.py": `import json
print(json.dumps({"result": "ok"}), end="")
`,
	})

	blockingFile := filepath.Join(root, "not-a-dir")
	testutil.MustWriteFile(t, blockingFile, "x")
	t.Setenv("INVOKR_TOOLBOX_DIR", root)
	t.Setenv("INVOKR_TOOLBOX_VENV_DIR", filepath.Join(blockingFile, "venv"))

	caller := NewCaller(
		core.CallerDeps{App: testAppConfigFromEnv(t)},
		zap.NewNop(),
	)
	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "python_no_requirements",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected direct python output ok, got %q", output)
	}
}

func TestNewCaller_RelativeToolboxDirInvocationWorks(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	projectDir := filepath.Join(t.TempDir(), "project")
	testutil.MustWriteToolboxPlugin(t, filepath.Join(projectDir, "toolbox"), "relative_echo", "Test relative toolbox dir", map[string]string{
		"main.go": `package main
import "fmt"
func main() { fmt.Print("{\"result\":\"ok-relative\"}") }`,
	})

	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir %s: %v", projectDir, err)
	}

	// Explicitly relative ("./"), not a bare name: a bare name resolves against
	// the Invokr home so agent-authored tools land in ~/.invokr/toolbox rather
	// than in whatever project the agent was started in. "./" opts into
	// CWD-relative resolution for the project-local dev workflow.
	t.Setenv("INVOKR_TOOLBOX_DIR", "./toolbox")

	caller := NewCaller(
		core.CallerDeps{App: testAppConfigFromEnv(t)},
		zap.NewNop(),
	)
	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "relative_echo",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}
	if output != "ok-relative" {
		t.Fatalf("expected ok-relative, got %q", output)
	}
}

func TestToolboxPluginReceivesAgentWorkingDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if filepath.Base(wd) != "tools" {
		t.Fatalf("expected test working directory to be tools, got %q", wd)
	}

	caller := NewCaller(
		core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: "../toolbox", AllowUnsandboxed: true}}},
		zap.NewNop(),
	)
	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "read",
		Arguments: json.RawMessage(`{"paths":["caller_toolbox_test.go"]}`),
	})
	if err != nil {
		t.Fatalf("tool call failed: %v", err)
	}
	if !strings.Contains(output, "TestToolboxPluginReceivesAgentWorkingDir") {
		t.Fatalf("expected read_file to resolve relative path from agent working dir")
	}
}

func TestReadFileUsesConfiguredWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, "README.md"), "workspace-canary")
	testutil.MustWriteFile(t, filepath.Join(root, ".hidden-note"), "hidden-canary")
	caller := NewCaller(
		core.CallerDeps{App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: "../toolbox", AllowUnsandboxed: true},
			Workspace: config.WorkspaceConfig{Root: root},
		}},
		zap.NewNop(),
	)
	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "read",
		Arguments: json.RawMessage(`{"paths":["README.md"]}`),
	})
	if err != nil || !strings.Contains(output, "workspace-canary") {
		t.Fatalf("expected configured workspace file, got output %q error %v", output, err)
	}
	_, err = caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "read",
		Arguments: json.RawMessage(`{"paths":[".hidden-note"]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "is hidden") {
		t.Fatalf("expected hidden path rejection, got %v", err)
	}
	caller = NewCaller(
		core.CallerDeps{App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: "../toolbox", AllowUnsandboxed: true},
			Workspace: config.WorkspaceConfig{Root: root, AllowHiddenPaths: true},
		}},
		zap.NewNop(),
	)
	output, err = caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "read",
		Arguments: json.RawMessage(`{"paths":[".hidden-note"]}`),
	})
	if err != nil || !strings.Contains(output, "hidden-canary") {
		t.Fatalf("expected configured hidden file, got output %q error %v", output, err)
	}
}

func TestReadFileToolSpecRequiresPathParameter(t *testing.T) {
	caller := NewCaller(
		core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: "../toolbox", AllowUnsandboxed: true}}},
		zap.NewNop(),
	)

	var spec core.ToolSpec
	for _, candidate := range caller.Specs() {
		if candidate.Name == "read" {
			spec = candidate
			break
		}
	}
	if spec.Name == "" {
		t.Fatal("expected read_file spec")
	}
	if spec.Parameters["additionalProperties"] != false {
		t.Fatalf("expected read_file parameters to disallow additional properties, got %#v", spec.Parameters)
	}
	// paths is the only argument: a list, one entry for a single file (#577).
	if required, _ := spec.Parameters["required"].([]string); len(required) != 1 || required[0] != "paths" {
		t.Fatalf("expected read to require paths, got %#v", spec.Parameters["required"])
	}
	properties, ok := spec.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected read properties map, got %#v", spec.Parameters["properties"])
	}
	if _, ok := properties["path"]; ok {
		t.Fatalf("read should not carry a single-path argument, got %#v", properties)
	}
	if paths, ok := properties["paths"].(map[string]any); !ok || paths["type"] != "array" {
		t.Fatalf("expected read paths array property, got %#v", properties["paths"])
	}
}

func TestReadFileToolIsCallableWithoutSchemaSearch(t *testing.T) {
	caller := NewCaller(
		core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: "../toolbox", AllowUnsandboxed: true}}},
		zap.NewNop(),
	)
	if !slices.ContainsFunc(caller.CallableSpecs("new-session"), func(spec core.ToolSpec) bool {
		return spec.Name == "read"
	}) {
		t.Fatal("expected read to be immediately callable")
	}
}

func TestReadFileToolMissingPathReturnsActionableError(t *testing.T) {
	caller := NewCaller(
		core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: "../toolbox", AllowUnsandboxed: true}}},
		zap.NewNop(),
	)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "read",
		Arguments: json.RawMessage(`{}`),
	})
	if err == nil || !strings.Contains(err.Error(), "paths is required") {
		t.Fatalf("expected actionable missing path error, got %v", err)
	}
}
