package core_test

import (
	"context"
	"encoding/json"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNoopResultFilterPreservesOutput(t *testing.T) {
	input := "line one\n\nline two\n"

	got, err := (core.NoopResultFilter{}).FilterToolResult(context.Background(), core.ToolResultMeta{
		ToolName: "read_file",
		Runtime:  "python",
		Source:   "toolbox",
	}, input)
	if err != nil {
		t.Fatalf("filter returned error: %v", err)
	}
	if got != input {
		t.Fatalf("expected output to be preserved exactly, got %q", got)
	}
}

func TestCallerHandleCallFiltersNativeToolResult(t *testing.T) {
	root := t.TempDir()
	fakeTokf := writeFakeTokfFilter(t, root)
	argsFile := filepath.Join(root, "args.txt")
	stdinFile := filepath.Join(root, "stdin.txt")
	t.Setenv("TOKF_ARGS_FILE", argsFile)
	t.Setenv("TOKF_STDIN_FILE", stdinFile)

	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox: config.ToolboxConfig{
			Dir:              t.TempDir(),
			TokfBin:          fakeTokf,
			AllowUnsandboxed: true,
		},
	}})
	caller.RegisterNative(core.ToolSpec{
		Name:        "native_echo",
		Description: "Echo native output.",
		Example:     `{"name":"native_echo","arguments":{}}`,
	}, func(context.Context, *core.ToolCall) (string, error) {
		return "native output", nil
	})

	got, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "native_echo"})
	if err != nil {
		t.Fatalf("HandleCall returned error: %v", err)
	}
	if got != "filtered: native output" {
		t.Fatalf("expected filtered native result, got %q", got)
	}
	if gotInput := mustReadFile(t, stdinFile); gotInput != "native output" {
		t.Fatalf("expected filter input to be raw native output, got %q", gotInput)
	}
	args := mustReadFile(t, argsFile)
	if !strings.Contains(args, "--tool\nnative_echo\n") || !strings.Contains(args, "--source\nnative\n") {
		t.Fatalf("expected native metadata in tokf args, got %q", args)
	}
}

func TestCallerHandleCallFiltersToolboxPluginResult(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "json_echo")
	mustWriteFile(t, filepath.Join(pluginDir, "tool.json"), `{
  "name":"json_echo",
  "description":"Return a JSON result.",
  "example":"{\"name\":\"json_echo\",\"arguments\":{}}",
  "namespace":"toolbox",
  "policy":{"tiers":["read_workspace"],"approval":"auto"},
  "capabilities":{"reads_workspace":false,"writes_workspace":false,"network":false,"executes_code":true}
}`)
	mustWriteFile(t, filepath.Join(pluginDir, "main.py"), `import json
print(json.dumps({"result": "parsed plugin output"}), end="")
`)

	fakeTokf := writeFakeTokfFilter(t, root)
	argsFile := filepath.Join(root, "args.txt")
	stdinFile := filepath.Join(root, "stdin.txt")
	t.Setenv("TOKF_ARGS_FILE", argsFile)
	t.Setenv("TOKF_STDIN_FILE", stdinFile)

	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox: config.ToolboxConfig{
			Dir:              root,
			TokfBin:          fakeTokf,
			AllowUnsandboxed: true,
		},
	}})

	got, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "json_echo",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("HandleCall returned error: %v", err)
	}
	if got != "filtered: parsed plugin output" {
		t.Fatalf("expected filtered plugin result, got %q", got)
	}
	if gotInput := mustReadFile(t, stdinFile); gotInput != "parsed plugin output" {
		t.Fatalf("expected filter input to be parsed plugin result, got %q", gotInput)
	}
	args := mustReadFile(t, argsFile)
	if !strings.Contains(args, "--tool\njson_echo\n") || !strings.Contains(args, "--source\ntoolbox\n") || !strings.Contains(args, "--runtime\npython\n") {
		t.Fatalf("expected toolbox metadata in tokf args, got %q", args)
	}
}
func TestCallerWithConfigUsesTokfFilterWhenEnabled(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "json_echo")
	mustWriteFile(t, filepath.Join(pluginDir, "tool.json"), `{
  "name":"json_echo",
  "description":"Return a JSON result.",
  "example":"{\"name\":\"json_echo\",\"arguments\":{}}",
  "namespace":"toolbox",
  "policy":{"tiers":["read_workspace"],"approval":"auto"},
  "capabilities":{"reads_workspace":false,"writes_workspace":false,"network":false,"executes_code":true}
}`)
	mustWriteFile(t, filepath.Join(pluginDir, "main.py"), `import json
print(json.dumps({"result": "parsed plugin output"}), end="")
`)

	fakeTokf := filepath.Join(root, "tokf")
	if err := os.WriteFile(fakeTokf, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'constructor filtered'\n"), 0755); err != nil {
		t.Fatalf("write fake tokf: %v", err)
	}

	app := config.AppConfig{Toolbox: config.ToolboxConfig{
		Dir:              root,
		TokfDisabled:     false,
		TokfBin:          fakeTokf,
		AllowUnsandboxed: true,
	}}.WithDefaults()
	caller := tools.NewCaller(core.CallerDeps{App: app})

	got, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "json_echo",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("HandleCall returned error: %v", err)
	}
	if got != "constructor filtered" {
		t.Fatalf("expected tokf-filtered plugin result, got %q", got)
	}
}

func writeFakeTokfFilter(t *testing.T, root string) string {
	t.Helper()
	fakeTokf := filepath.Join(root, "tokf")
	if err := os.WriteFile(fakeTokf, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TOKF_ARGS_FILE\"\ninput=$(cat)\nprintf '%s' \"$input\" > \"$TOKF_STDIN_FILE\"\nprintf 'filtered: %s' \"$input\"\n"), 0755); err != nil {
		t.Fatalf("write fake tokf: %v", err)
	}
	return fakeTokf
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func TestCallerWithConfigUsesNoopFilterWhenDisabled(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "json_echo")
	mustWriteFile(t, filepath.Join(pluginDir, "tool.json"), `{
  "name":"json_echo",
  "description":"Return a JSON result.",
  "example":"{\"name\":\"json_echo\",\"arguments\":{}}",
  "namespace":"toolbox",
  "policy":{"tiers":["read_workspace"],"approval":"auto"},
  "capabilities":{"reads_workspace":false,"writes_workspace":false,"network":false,"executes_code":true}
}`)
	mustWriteFile(t, filepath.Join(pluginDir, "main.py"), `import json
print(json.dumps({"result": "parsed plugin output"}), end="")
`)

	fakeTokf := filepath.Join(root, "tokf")
	if err := os.WriteFile(fakeTokf, []byte("#!/bin/sh\nprintf 'should not run'\n"), 0755); err != nil {
		t.Fatalf("write fake tokf: %v", err)
	}
	app := config.AppConfig{Toolbox: config.ToolboxConfig{
		Dir:              root,
		TokfDisabled:     true,
		TokfBin:          fakeTokf,
		AllowUnsandboxed: true,
	}}.WithDefaults()
	caller := tools.NewCaller(core.CallerDeps{App: app})

	got, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "json_echo",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("HandleCall returned error: %v", err)
	}
	if got != "parsed plugin output" {
		t.Fatalf("expected unfiltered plugin result, got %q", got)
	}
}

func TestCallerWithConfigUsesNoopFilterWhenTokfMissing(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "json_echo")
	mustWriteFile(t, filepath.Join(pluginDir, "tool.json"), `{
  "name":"json_echo",
  "description":"Return a JSON result.",
  "example":"{\"name\":\"json_echo\",\"arguments\":{}}",
  "namespace":"toolbox",
  "policy":{"tiers":["read_workspace"],"approval":"auto"},
  "capabilities":{"reads_workspace":false,"writes_workspace":false,"network":false,"executes_code":true}
}`)
	mustWriteFile(t, filepath.Join(pluginDir, "main.py"), `import json
print(json.dumps({"result": "parsed plugin output"}), end="")
`)

	app := config.AppConfig{Toolbox: config.ToolboxConfig{
		Dir:              root,
		TokfDisabled:     true,
		TokfBin:          filepath.Join(root, "missing-tokf"),
		AllowUnsandboxed: true,
	}}.WithDefaults()
	caller := tools.NewCaller(core.CallerDeps{App: app})

	got, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "json_echo",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("HandleCall returned error: %v", err)
	}
	if got != "parsed plugin output" {
		t.Fatalf("expected unfiltered fallback result, got %q", got)
	}
}
