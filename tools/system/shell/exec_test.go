package shell_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
	"github.com/invokr-agent/invokr/tools/system/shell"
)

// sandboxCaller builds a caller whose workspace is root, with every write
// auto-approved so the sweep exercises the sandbox, not the approval layer.
// Network commands stay disallowed, as in a default config, so the sandbox
// keeps its own network namespace.
func sandboxCaller(t *testing.T, root string, allowed ...string) *tools.Caller {
	t.Helper()
	return sandboxCallerNet(t, root, false, allowed...)
}

// sandboxCallerNet is sandboxCaller with allow_network_commands under test.
func sandboxCallerNet(t *testing.T, root string, allowNetwork bool, allowed ...string) *tools.Caller {
	t.Helper()
	return tools.NewCaller(core.CallerDeps{
		Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: true}},
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			Shell:     config.ShellConfig{AllowedCommands: allowed, AllowWriteCommands: true, AllowNetworkCommands: allowNetwork},
		},
	})
}

func runLine(t *testing.T, caller *tools.Caller, line string) (shell.ShellResult, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"command": line})
	out, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "shell", Arguments: raw})
	var toolErr *core.ToolError
	if err != nil {
		if !errors.As(err, &toolErr) || toolErr.Payload == "" {
			return shell.ShellResult{}, err
		}
		out = toolErr.Payload
	}
	return shell.ParseText(out), nil
}

// Shapes the argv-only tool refused by name now run, byte-for-byte what a
// host bash would print in the same directory.
func TestShellRunsBashShapes(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "logs", "a.log"), "ERROR a\nINFO b\nERROR c\n")
	mustWriteFile(t, filepath.Join(root, "logs", "b.log"), "ERROR d\n")
	mustWriteFile(t, filepath.Join(root, "x.sh"), "#!/bin/sh\necho ran; exit 3\n")
	if err := os.Chmod(filepath.Join(root, "x.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sandbox.AllowUnsandboxedEnv, "1")
	caller := sandboxCaller(t, root, "cat", "echo", "wc", "grep", "sort", "tail", "ls", "head")
	cases := []struct{ line, stdout string }{
		{"cat logs/a.log; echo ---; wc -l < logs/a.log", "ERROR a\nINFO b\nERROR c\n---\n3"},
		{"./x.sh; echo \"exit=$?\"", "ran\nexit=3"},
		{"for f in logs/*.log; do echo \"== $f\"; cat \"$f\"; done", "== logs/a.log\nERROR a\nINFO b\nERROR c\n== logs/b.log\nERROR d"},
		{"grep -h -c ERROR logs/*.log | sort -n | tail -1", "2"},
		{"cat > out.txt <<'EOF'\nhello\nEOF\ncat out.txt", "hello"},
		{"echo hi > /tmp/scratch.txt && cat /tmp/scratch.txt", "hi"},
		{"N=$(ls logs | wc -l); echo \"n=$N\"", "n=2"},
		{"if [ -f logs/a.log ]; then echo yes; fi", "yes"},
	}
	for _, c := range cases {
		res, err := runLine(t, caller, c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if res.Stdout != c.stdout {
			t.Fatalf("%q: stdout %q want %q (stderr %q)", c.line, res.Stdout, c.stdout, res.Stderr)
		}
		if res.Cwd != "." || strings.Contains(res.Cwd, root) {
			t.Fatalf("%q: cwd %q must be workspace-relative", c.line, res.Cwd)
		}
	}
}

// The sandbox, not the walk, is the guarantee: with every write approved and
// every command allowed, a line still cannot write outside the workspace,
// read the home directory, or reach the network.
func TestShellSandboxSecuritySweep(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable on this host")
	}
	root := t.TempDir()
	outside := t.TempDir()
	home := t.TempDir()
	mustWriteFile(t, filepath.Join(home, ".secret"), "s3cret")
	t.Setenv("HOME", home)
	t.Setenv("LEAKED_TOKEN", "hunter2")
	caller := sandboxCaller(t, root, "cat", "echo", "ls", "touch", "cp", "mkdir", "env", "bash", "python3", "curl")

	mustFail := []string{
		"echo x > " + filepath.Join(outside, "escape.txt"),
		"cp /etc/hostname " + filepath.Join(outside, "escape2.txt"),
		"cat " + filepath.Join(home, ".secret"),
		"cat ~/.secret",
		"echo x > /usr/escape",
		"ls " + outside,
		"bash -c 'echo x > " + filepath.Join(outside, "escape3.txt") + "'",
		// curl/wget are network tier (#545), so an approved curl legitimately
		// keeps the host network. A write-tier line must still find none.
		"python3 -c 'import urllib.request; urllib.request.urlopen(\"http://example.com/\", timeout=2)'",
	}
	for _, line := range mustFail {
		res, err := runLine(t, caller, line)
		if err == nil && res.ExitCode == 0 {
			t.Fatalf("%q must fail inside the sandbox, got %+v", line, res)
		}
	}
	// The sandbox root is remounted read-only after setup, so a write outside
	// a mounted RW path fails loudly instead of vanishing into a tmpdir that
	// is deleted at exit — the phantom `/data/output` the model debugged for
	// ten turns.
	for _, line := range []string{"mkdir /data", "echo x > /data/x", "echo x > /etc/escape-" + filepath.Base(root)} {
		res, err := runLine(t, caller, line)
		if err == nil && res.ExitCode == 0 {
			t.Fatalf("%q must fail on the read-only sandbox root, got %+v", line, res)
		}
	}
	for _, path := range []string{filepath.Join(outside, "escape.txt"), filepath.Join(outside, "escape2.txt"), filepath.Join(outside, "escape3.txt"), "/etc/escape-" + filepath.Base(root)} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%s was written from inside the sandbox", path)
		}
	}
	res, err := runLine(t, caller, "env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "hunter2") {
		t.Fatalf("host env leaked into the sandbox: %s", res.Stdout)
	}
	res, err = runLine(t, caller, "echo x > inside.txt && cat inside.txt")
	if err != nil || res.Stdout != "x" {
		t.Fatalf("workspace write must work: %+v %v", res, err)
	}
	// The model-facing text no longer repeats "sandboxed" per call (the tool
	// description states it); the sweep above is what proves the sandbox.
}

// A workspace-relative cwd is honoured, an absolute host path is refused,
// and the result reports where the line ran without the host path.
func TestShellCwdIsWorkspaceRelative(t *testing.T) {
	t.Setenv(sandbox.AllowUnsandboxedEnv, "1")
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "sub", "f"), "in-sub\n")
	caller := sandboxCaller(t, root, "cat", "pwd")
	raw, _ := json.Marshal(map[string]any{"command": "cat f", "cwd": "sub"})
	out, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "shell", Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	res := shell.ParseText(out)
	if res.Stdout != "in-sub" || res.Cwd != "sub" {
		t.Fatalf("got %+v", res)
	}
	raw, _ = json.Marshal(map[string]any{"command": "cat f", "cwd": "/nonexistent/abs"})
	if _, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "shell", Arguments: raw}); err == nil {
		t.Fatal("absolute cwd outside the workspace must be refused")
	}
}

// With network commands allowed the sandbox shares the host network namespace,
// so curl reaches a host-local server instead of failing to resolve/connect
// (#543). Loopback stands in for DNS: the test must not need the internet.
func TestShellNetworkAllowedReachesHostServer(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable on this host")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("PK-payload"))
	}))
	defer srv.Close()
	res, err := runLine(t, sandboxCallerNet(t, t.TempDir(), true, "curl"), "curl -sS "+srv.URL)
	if err != nil {
		t.Fatalf("curl: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "PK-payload" {
		t.Fatalf("curl did not reach the host server: %+v", res)
	}
}

// A JVM starts inside the sandbox: the JDK's conf directory is a symlink into
// /etc on every mainstream distribution, and unmounted it made every java
// process die on "Error loading java.security file" before main (#546).
func TestShellSandboxStartsAJVM(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable on this host")
	}
	if _, err := exec.LookPath("java"); err != nil {
		t.Skip("java not installed")
	}
	res, err := runLine(t, sandboxCaller(t, t.TempDir(), "java"), "java -version")
	if err != nil {
		t.Fatalf("java -version: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("java -version exited %d: stdout %q stderr %q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

// gradle needs a writable GRADLE_USER_HOME to unpack its native libraries
// into; read-only it dies on "Failed to load native library" (#546).
func TestShellSandboxRunsGradle(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable on this host")
	}
	if _, err := exec.LookPath("gradle"); err != nil {
		t.Skip("gradle not installed")
	}
	res, err := runLine(t, sandboxCaller(t, t.TempDir(), "gradle"), "gradle --version --offline")
	if err != nil {
		t.Fatalf("gradle --version: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("gradle --version exited %d: stdout %q stderr %q", res.ExitCode, res.Stdout, res.Stderr)
	}
}
