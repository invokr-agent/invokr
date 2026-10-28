package shell_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
)

func outputTestCaller(t *testing.T, allowed ...string) *tools.Caller {
	t.Helper()
	return tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: t.TempDir()},
			Shell:     config.ShellConfig{AllowedCommands: allowed, AllowWriteCommands: true},
		},
	})
}

// A command that prints nothing must say so: an empty stdout field read to the
// model as a truncation or a broken tool, and it retried a call that had
// already succeeded.
func TestShellToolSilentCommandSaysNoOutput(t *testing.T) {
	output, err := outputTestCaller(t, "echo").HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"echo -n"}`),
	})
	if err != nil {
		t.Fatalf("run silent command: %v", err)
	}
	result := decodeShellResult(t, output)
	if result.Stdout != constants.ShellNoOutputMarker {
		t.Fatalf("expected %q, got %#v", constants.ShellNoOutputMarker, result)
	}
}

// Silence is what the marker answers: a command that spoke on stderr leaves
// stdout empty, so the model is not told there was output where there was none.
func TestShellToolNoOutputMarkerSkippedWhenStderrSpoke(t *testing.T) {
	_, err := outputTestCaller(t, "ls").HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"ls definitely-not-a-path"}`),
	})
	var te *core.ToolError
	if !errors.As(err, &te) || te.Payload == "" {
		t.Fatalf("expected flagged result with payload, got %v", err)
	}
	result := decodeShellResult(t, te.Payload)
	if result.Stdout != "" || result.Stderr == "" {
		t.Fatalf("expected empty stdout beside a stderr message, got %#v", result)
	}
}

// The line runs under bash, so a pipeline's filters see the whole stream and
// only the last stage's output meets max_output_bytes. The bug this guards
// against (#440 M1) was counting a truncated fragment: `seq | wc -l` must
// report every line, not the ones that fit in the model's budget.
func TestShellToolTruncatesAfterThePipelineNotBefore(t *testing.T) {
	output, err := outputTestCaller(t, "seq", "wc").HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"seq 1 100000 | wc -l","max_output_bytes":32}`),
	})
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	result := decodeShellResult(t, output)
	if strings.TrimSpace(result.Stdout) != "100000" {
		t.Fatalf("expected the full stream counted, got %#v", result)
	}
	if result.Truncated {
		t.Fatalf("the last stage fits the budget, so nothing should be truncated: %#v", result)
	}
}
