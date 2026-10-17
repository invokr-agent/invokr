package shell_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/shell"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/testutil"
)

func hasSpec(specs []core.ToolSpec, name string) bool {
	return slices.ContainsFunc(specs, func(spec core.ToolSpec) bool {
		return spec.Name == name
	})
}
func TestCallerRegistersShellToolWhenEnabled(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	if !hasSpec(caller.Specs(), "shell") {
		t.Fatal("expected shell tool to be registered")
	}
}

func TestCallerOmitsShellToolWhenDisabled(t *testing.T) {
	disabled := false
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Shell:   config.ShellConfig{Enabled: &disabled},
		},
	})

	if hasSpec(caller.Specs(), "shell") {
		t.Fatal("expected shell tool to be omitted")
	}
}

func TestShellToolAllowsConfiguredCommands(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"git","args":["--version"]}`),
	})
	if err != nil {
		t.Fatalf("expected git to be allowed: %v", err)
	}
	result := decodeShellResult(t, output)
	if !strings.Contains(result.Stdout, "git version") {
		t.Fatalf("expected git version output, got %q", output)
	}
}

func TestShellToolRunsFromAgentWorkingDir(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"ls","args":["."],"cwd":"."}`),
	})
	if err != nil {
		t.Fatalf("expected ls to be allowed: %v", err)
	}
	result := decodeShellResult(t, output)
	if !strings.Contains(result.Stdout, "shell.go") {
		t.Fatalf("expected shell ls to run from agent working dir, got %q", output)
	}
}

func TestShellToolAllowsGoCommandsByDefault(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"go","args":["version"]}`),
	})
	if err != nil {
		t.Fatalf("expected go to be allowed by default: %v", err)
	}
	result := decodeShellResult(t, output)
	if !strings.Contains(result.Stdout, "go version") {
		t.Fatalf("expected go version output, got %q", output)
	}
}

func TestShellToolAllowsExplicitlyConfiguredCommand(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Shell:   config.ShellConfig{AllowedCommands: []string{"go"}},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"go","args":["version"]}`),
	})
	if err != nil {
		t.Fatalf("expected configured go command to be allowed: %v", err)
	}
	result := decodeShellResult(t, output)
	if !strings.Contains(result.Stdout, "go version") {
		t.Fatalf("expected go version output, got %q", output)
	}
}

func TestNativeShellToolWinsOverToolboxShellPlugin(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "shell")
	mustWriteFile(t, filepath.Join(pluginDir, "tool.json"), `{
  "name":"shell",
  "description":"Plugin shell replacement.",
  "example":"{\"name\":\"shell\",\"arguments\":{}}"
}`)
	mustWriteFile(t, filepath.Join(pluginDir, "main.py"), `import json
print(json.dumps({"result": "plugin shell"}), end="")
`)

	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: root},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"git","args":["--version"]}`),
	})
	if err != nil {
		t.Fatalf("expected native shell to run: %v", err)
	}
	if output == "plugin shell" {
		t.Fatal("expected native shell to win over toolbox shell plugin")
	}
}

func TestShellToolDescriptionIncludesConfiguredAllowlist(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Shell:   config.ShellConfig{AllowedCommands: []string{"git", "ls", "go"}},
		},
	})

	var shellSpec core.ToolSpec
	for _, spec := range caller.Specs() {
		if spec.Name == "shell" {
			shellSpec = spec
			break
		}
	}
	if shellSpec.Name == "" {
		t.Fatal("expected shell spec")
	}
	// The configured commands, plus the read-only baseline that is always
	// unioned in, sorted.
	if !strings.Contains(shellSpec.Description, "git, go, grep, head, ls") {
		t.Fatalf("expected sorted allowlist in shell description, got %q", shellSpec.Description)
	}
}

func TestShellToolRejectsCWDOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"pwd","args":[],"cwd":"` + outside + `"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "is outside the workspace") {
		t.Fatalf("expected cwd rejection, got %v", err)
	}
}

func TestShellToolTimeoutWorks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	mustWriteFile(t, filepath.Join(dir, "slow"), "#!/bin/sh\nsleep 1\n")
	if err := os.Chmod(filepath.Join(dir, "slow"), 0o755); err != nil {
		t.Fatalf("chmod slow: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)

	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			Shell: config.ShellConfig{
				AllowedCommands:    []string{"slow"},
				AllowWriteCommands: true,
			},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"slow","args":[],"timeout_ms":10}`),
	})
	var te *core.ToolError
	if !errors.As(err, &te) || te.Payload == "" {
		t.Fatalf("expected ToolError with payload for timeout, got %v", err)
	}
	result := decodeShellResult(t, te.Payload)
	if !result.TimedOut {
		t.Fatalf("expected timeout result, got %#v", result)
	}
}

func TestShellToolOutputTruncates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	mustWriteFile(t, filepath.Join(dir, "loud"), "#!/bin/sh\nprintf '1234567890'\n")
	if err := os.Chmod(filepath.Join(dir, "loud"), 0o755); err != nil {
		t.Fatalf("chmod loud: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)

	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			Shell: config.ShellConfig{
				AllowedCommands:    []string{"loud"},
				AllowWriteCommands: true,
			},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"loud","args":[],"max_output_bytes":4}`),
	})
	if err != nil {
		t.Fatalf("expected truncation to succeed, got %v", err)
	}
	result := decodeShellResult(t, output)
	if !result.Truncated || result.Stdout != "1234" {
		t.Fatalf("expected truncated output, got %#v", result)
	}
}

func TestShellToolNonzeroReturnsStructuredResult(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"ls","args":["definitely-not-a-path"]}`),
	})
	var te *core.ToolError
	if !errors.As(err, &te) || te.Payload == "" {
		t.Fatalf("expected ToolError with payload for nonzero exit, got %v", err)
	}
	if te.Code == "" {
		t.Fatalf("expected exit code label, got %#v", te)
	}
	result := decodeShellResult(t, te.Payload)
	if result.ExitCode == 0 || result.Stderr == "" {
		t.Fatalf("expected structured nonzero result, got %#v", result)
	}
}

// grep exiting 1 means "no match", an answer rather than a failure: it must
// not come back flagged as a ToolError, or the model reads a working search
// as broken and the ledger counts a non-failure.
func TestShellToolBenignExitIsNotFlagged(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	out, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"grep","args":["no-such-needle","shell.go"]}`),
	})
	if err != nil {
		t.Fatalf("grep exit 1 must not be flagged: %v", err)
	}
	result := decodeShellResult(t, out)
	if result.ExitCode != 1 {
		t.Fatalf("expected exit 1 in the structured result, got %#v", result)
	}
}

func TestShellToolWriteTierRequiresApproval(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"go","args":["test","./..."]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected write tier rejection, got %v", err)
	}
}

func TestShellToolWriteTierRunsAfterApproval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	mustWriteFile(t, filepath.Join(dir, "writer"), "#!/bin/sh\nprintf 'approved'\n")
	if err := os.Chmod(filepath.Join(dir, "writer"), 0o755); err != nil {
		t.Fatalf("chmod writer: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)

	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			Shell:     config.ShellConfig{AllowedCommands: []string{"writer"}},
		},
		Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: true}},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"writer","args":[]}`),
	})
	if err != nil {
		t.Fatalf("expected approved write-tier command, got %v", err)
	}
	result := decodeShellResult(t, output)
	if result.Stdout != "approved" {
		t.Fatalf("expected approved stdout, got %#v", result)
	}
}

func TestShellToolNetworkTierRequiresApproval(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"git","args":["fetch"]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected network tier rejection, got %v", err)
	}
}

func TestShellBackgroundProcessCanWaitAndReadLog(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_HOME", filepath.Join(root, "invokr-home"))
	dir := filepath.Join(root, "bin")
	mustWriteFile(t, filepath.Join(dir, "bgjob"), "#!/bin/sh\nprintf 'background-output'\n")
	if err := os.Chmod(filepath.Join(dir, "bgjob"), 0o755); err != nil {
		t.Fatalf("chmod bgjob: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			Shell: config.ShellConfig{
				AllowedCommands:    []string{"bgjob"},
				AllowWriteCommands: true,
			},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"bgjob","args":[],"cwd":".","background":true}`),
	})
	if err != nil {
		t.Fatalf("start background: %v", err)
	}
	var started struct {
		ProcessID string `json:"process_id"`
	}
	if err := json.Unmarshal([]byte(output), &started); err != nil || started.ProcessID == "" {
		t.Fatalf("bad background result %q: %v", output, err)
	}
	if strings.Contains(output, filepath.Join(root, ".invokr")) || !strings.Contains(output, filepath.Join(root, "invokr-home", "process")) {
		t.Fatalf("background log was not relocated out of workspace: %s", output)
	}
	waitOut, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "process",
		Arguments: json.RawMessage(`{"action":"wait","process_id":"` + started.ProcessID + `","timeout_ms":1000}`),
	})
	if err != nil {
		t.Fatalf("wait process: %v", err)
	}
	if !strings.Contains(waitOut, `"done":true`) {
		t.Fatalf("expected done process, got %s", waitOut)
	}
	logOut, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "process",
		Arguments: json.RawMessage(`{"action":"log","process_id":"` + started.ProcessID + `"}`),
	})
	if err != nil {
		t.Fatalf("read process log: %v", err)
	}
	if !strings.Contains(logOut, "background-output") {
		t.Fatalf("expected process log, got %s", logOut)
	}
}

// Background commands must honor the same host-env isolation as the
// foreground path: secrets stay invisible unless isolation is disabled.
func TestShellBackgroundProcessIsolatesHostEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_HOME", filepath.Join(root, "invokr-home"))
	t.Setenv("INVOKR_TEST_SECRET", "s3cret-value")
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			Shell:     config.ShellConfig{AllowedCommands: []string{"env"}, AllowWriteCommands: true},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"env","args":[],"background":true}`),
	})
	if err != nil {
		t.Fatalf("start background: %v", err)
	}
	var started shell.BackgroundResult
	if err := json.Unmarshal([]byte(output), &started); err != nil || started.ProcessID == "" {
		t.Fatalf("bad background result %q: %v", output, err)
	}
	if _, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "process",
		Arguments: json.RawMessage(`{"action":"wait","process_id":"` + started.ProcessID + `","timeout_ms":5000}`),
	}); err != nil {
		t.Fatalf("wait process: %v", err)
	}
	logOut, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "process",
		Arguments: json.RawMessage(`{"action":"log","process_id":"` + started.ProcessID + `","max_bytes":200000}`),
	})
	if err != nil {
		t.Fatalf("read process log: %v", err)
	}
	if strings.Contains(logOut, "INVOKR_TEST_SECRET=s3cret-value") {
		t.Fatalf("host secret leaked into background env:\n%s", logOut)
	}
	if !strings.Contains(logOut, "PATH=") {
		t.Fatalf("PATH missing from background env:\n%s", logOut)
	}
}

func TestShellBackgroundProcessIsSessionScoped(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_HOME", filepath.Join(root, "invokr-home"))
	dir := filepath.Join(root, "bin")
	mustWriteFile(t, filepath.Join(dir, "bgjob"), "#!/bin/sh\nsleep 5\n")
	if err := os.Chmod(filepath.Join(dir, "bgjob"), 0o755); err != nil {
		t.Fatalf("chmod bgjob: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
		Workspace: config.WorkspaceConfig{Root: root},
		Shell:     config.ShellConfig{AllowedCommands: []string{"bgjob"}, AllowWriteCommands: true},
	}})
	sessionA := core.WithSessionID(context.Background(), "session-a")
	output, err := caller.HandleCall(sessionA, &core.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"bgjob","args":[],"background":true}`)})
	if err != nil {
		t.Fatalf("start background: %v", err)
	}
	var started shell.BackgroundResult
	if err := json.Unmarshal([]byte(output), &started); err != nil {
		t.Fatalf("decode background result: %v", err)
	}
	foreign := core.WithSessionID(context.Background(), "session-b")
	_, err = caller.HandleCall(foreign, &core.ToolCall{Name: "process", Arguments: json.RawMessage(`{"action":"log","process_id":"` + started.ProcessID + `"}`)})
	if err == nil || !strings.Contains(err.Error(), "process not found in this session") {
		t.Fatalf("expected session ownership error, got %v", err)
	}
	_, err = caller.HandleCall(foreign, &core.ToolCall{Name: "process", Arguments: json.RawMessage(`{"action":"kill","process_id":"` + started.ProcessID + `"}`)})
	if err == nil || !strings.Contains(err.Error(), "process not found in this session") {
		t.Fatalf("expected session ownership error, got %v", err)
	}
	if _, err := caller.HandleCall(sessionA, &core.ToolCall{Name: "process", Arguments: json.RawMessage(`{"action":"kill","process_id":"` + started.ProcessID + `"}`)}); err != nil {
		t.Fatalf("owner kill process: %v", err)
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
func TestShellToolCanAllowWriteTierByConfig(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	mustWriteFile(t, filepath.Join(dir, "writer"), "#!/bin/sh\nprintf 'ok'\n")
	if err := os.Chmod(filepath.Join(dir, "writer"), 0o755); err != nil {
		t.Fatalf("chmod writer: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)

	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			Shell: config.ShellConfig{
				AllowedCommands:    []string{"writer"},
				AllowWriteCommands: true,
			},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"writer","args":[]}`),
	})
	if err != nil {
		t.Fatalf("expected write-tier config approval, got %v", err)
	}
	result := decodeShellResult(t, output)
	if result.Stdout != "ok" {
		t.Fatalf("expected writer stdout, got %#v", result)
	}
}

func TestShellToolEmitsAuditEvent(t *testing.T) {
	root := t.TempDir()
	dispatcher := events.NewOrderedDispatcher(nil)
	var seen events.Event
	dispatcher.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventAudit {
			seen = ev
		}
		return nil
	}))

	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
		Dispatcher: dispatcher,
	})

	output, err := caller.HandleCall(core.WithSessionID(context.Background(), "shell-session"), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"pwd","args":[]}`),
	})
	if err != nil {
		t.Fatalf("expected shell success, got %v", err)
	}
	_ = decodeShellResult(t, output)
	if seen.Type != events.EventAudit || seen.Audit == nil {
		t.Fatal("expected audit event")
	}
	if seen.SessionID != "shell-session" {
		t.Fatalf("expected session id, got %q", seen.SessionID)
	}
	if seen.Audit.Tool != "shell" || seen.Audit.Outcome != "completed" {
		t.Fatalf("expected completed shell audit, got %#v", seen.Audit)
	}
}

func decodeShellResult(t *testing.T, output string) shell.ShellResult {
	t.Helper()
	return shell.ParseText(output)
}

func TestShellToolRecoversFullCommandLineInCommand(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"git --version","args":[]}`),
	})
	if err != nil {
		t.Fatalf("expected full command line to be split and run: %v", err)
	}
	result := decodeShellResult(t, output)
	if !strings.Contains(result.Stdout, "git version") {
		t.Fatalf("expected git version output, got %q", output)
	}
}

func TestShellToolCommandLineRecoveryKeepsAllowlistClosed(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"curl https://example.com","args":[]}`),
	})
	if err == nil {
		t.Fatal("expected disallowed command line to be rejected")
	}
	if !strings.Contains(err.Error(), `"curl" is not an allowed executable`) {
		t.Fatalf("expected the allowlist refusal in error, got %v", err)
	}
}

func TestShellToolRejectsQuotedCommandLine(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	// A quoted line is bash: with no approver configured the write-tier call
	// surfaces an approval error, proving it got past the policy walk.
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"git commit -m \"two words\"","args":[]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected quoted line to reach approval, got %v", err)
	}

	// A redirect that writes a file is a write, named as the reason.
	_, err = caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"ls -la > out.txt","args":[]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "redirects output to a file") {
		t.Fatalf("expected the redirect named as the approval reason, got %v", err)
	}
}

// requireApprovalCase runs a shell call with no approver configured: readonly
// commands run, anything else fails with a requires-approval error.
func requireApprovalCase(t *testing.T, arguments string, wantApproval bool) {
	t.Helper()
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "shell",
		Arguments: json.RawMessage(arguments),
	})
	if wantApproval {
		if err == nil || !strings.Contains(err.Error(), "requires approval") {
			t.Fatalf("expected requires-approval error, got %v", err)
		}
		return
	}
	if err != nil && strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected readonly execution without approval, got %v", err)
	}
}

func TestShellTierGoEnvWriteRequiresApproval(t *testing.T) {
	requireApprovalCase(t, `{"command":"go","args":["env","-w","GOFLAGS="]}`, true)
	requireApprovalCase(t, `{"command":"go","args":["env","-u","GOFLAGS"]}`, true)
	requireApprovalCase(t, `{"command":"go","args":["env","GOPATH"]}`, false)
}

func TestShellTierGitBranchWithArgsRequiresApproval(t *testing.T) {
	requireApprovalCase(t, `{"command":"git","args":["branch","-D","doomed"]}`, true)
	requireApprovalCase(t, `{"command":"git","args":["branch","new-branch"]}`, true)
	requireApprovalCase(t, `{"command":"git","args":["branch"]}`, false)
	requireApprovalCase(t, `{"command":"git","args":["branch","-a"]}`, false)
	requireApprovalCase(t, `{"command":"git","args":["branch","--list","feature/*"]}`, false)
	requireApprovalCase(t, `{"command":"git","args":["stash","list"]}`, false)
}

func TestShellTierGoVetIsReadOnly(t *testing.T) {
	requireApprovalCase(t, `{"command":"go","args":["vet","./..."]}`, false)
}

func TestShellTierGitOutputFlagRequiresApproval(t *testing.T) {
	requireApprovalCase(t, `{"command":"git","args":["log","-1","--output=/tmp/x"]}`, true)
	requireApprovalCase(t, `{"command":"git","args":["log","-1"]}`, false)
}

// Host env isolation: secrets are invisible by default, EnvPassthrough exposes
// named vars, and isolate_env=false restores full inheritance (finding #4).
func TestShellToolHostEnvIsolation(t *testing.T) {
	inherit := false
	cases := []struct {
		name       string
		shell      config.ShellConfig
		wantSecret bool
	}{
		{
			name:       "isolated by default",
			shell:      config.ShellConfig{},
			wantSecret: false,
		},
		{
			name:       "passthrough exposes named var",
			shell:      config.ShellConfig{EnvPassthrough: []string{"INVOKR_TEST_SECRET"}},
			wantSecret: true,
		},
		{
			name:       "isolate_env=false inherits host env",
			shell:      config.ShellConfig{IsolateEnv: &inherit},
			wantSecret: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INVOKR_TEST_SECRET", "s3cret-value")
			tc.shell.AllowedCommands = []string{"env"}
			tc.shell.AllowWriteCommands = true
			caller := tools.NewCaller(core.CallerDeps{
				App: config.AppConfig{
					Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
					Shell:   tc.shell,
				},
			})
			// max_output_bytes raised above the default 20000: CI injects a
			// CI_PIPELINE_FILES var listing every changed file, which alone
			// can exceed the default cap on large diffs and truncate the
			// secret out of this test's `env` dump before it's asserted on.
			output, err := caller.HandleCall(context.Background(), &core.ToolCall{
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"env","args":[],"max_output_bytes":200000}`),
			})
			if err != nil {
				t.Fatalf("HandleCall() error = %v", err)
			}
			result := decodeShellResult(t, output)
			gotSecret := strings.Contains(result.Stdout, "INVOKR_TEST_SECRET=s3cret-value")
			if gotSecret != tc.wantSecret {
				t.Fatalf("secret visible = %v, want %v; stdout:\n%s", gotSecret, tc.wantSecret, result.Stdout)
			}
			if !strings.Contains(result.Stdout, "PATH=") {
				t.Fatalf("PATH missing from shell env; stdout:\n%s", result.Stdout)
			}
		})
	}
}
