package shell_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
)

func TestShellBuildFindsSymlinkedUserTool(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(sandbox.AllowUnsandboxedEnv, "0")
	bin := filepath.Join(home, ".local", "bin")
	target := filepath.Join(home, "toolchain", "bin", "fixture-node")
	mustWriteFile(t, target, "#!/bin/sh\nprintf 'BUILD_OK'\n")
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(bin, "fixture-node")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, "unlisted", "bin", "other-tool")
	mustWriteFile(t, other, "unlisted secret")
	if err := os.Chmod(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(bin, "other-tool")); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(home, ".local", "private-secret")
	mustWriteFile(t, secret, "private")
	nearSecret := filepath.Join(home, "toolchain", "secret")
	mustWriteFile(t, nearSecret, "same-prefix secret")
	nearTool := filepath.Join(filepath.Dir(target), "unlisted-tool")
	mustWriteFile(t, nearTool, "#!/bin/sh\nprintf UNLISTED\n")
	if err := os.Chmod(nearTool, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "Makefile"), "all:\n\t@fixture-node\n"+
		"private:\n\t@test ! -e "+other+" && test ! -e "+secret+" && test ! -e "+nearSecret+" && test ! -e "+nearTool+" && printf PRIVATE_OK\n"+
		"readonly:\n\t@if printf changed > "+target+"; then exit 1; fi; fixture-node\n")
	caller := sandboxCaller(t, root, "make", "fixture-node")
	for _, tc := range []struct{ line, output string }{
		{"fixture-node", "BUILD_OK"}, {"make", "BUILD_OK"},
		{"echo diagnostic >/dev/null; make", "BUILD_OK"},
		{"make private", "PRIVATE_OK"}, {"make readonly", "BUILD_OK"},
		{"echo diagnostic >/dev/null; make private", "PRIVATE_OK"},
		// Prior mounts can leave an empty mountpoint; executable bytes must stay hidden.
		{"test ! -s " + target + " && printf HIDDEN", "HIDDEN"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			result, err := runLine(t, caller, tc.line)
			if err != nil || result.ExitCode != 0 || result.Stdout != tc.output {
				t.Fatalf("%s: result=%+v error=%v", tc.line, result, err)
			}
		})
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "#!/bin/sh\nprintf 'BUILD_OK'\n" {
		t.Fatalf("toolchain modified: %q, %v", content, err)
	}
}

func TestShellMakeStillRequiresApproval(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "Makefile"), "all:\n\t@printf ran > marker\n")
	caller := tools.NewCaller(core.CallerDeps{
		Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: false}},
		App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			Shell:     config.ShellConfig{AllowedCommands: []string{"make"}}},
	})
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "shell", Arguments: []byte(`{"command":"make"}`)})
	var denied *approval.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("wanted denied approval, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
		t.Fatalf("denied recipe executed: %v", err)
	}
}

func TestShellMakeUserToolPreservesPrivateTmp(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	if err := os.Mkdir("/tmp/bin", 0o700); err == nil {
		t.Cleanup(func() { _ = os.Remove("/tmp/bin") })
	} else if !os.IsExist(err) {
		t.Fatal(err)
	}
	if info, err := os.Lstat("/tmp/bin"); err != nil || !info.IsDir() {
		t.Skip("/tmp/bin is not an ordinary directory")
	}
	tool, err := os.CreateTemp("/tmp/bin", "invokr-tool-*")
	if err != nil {
		t.Fatal(err)
	}
	path := tool.Name()
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := tool.WriteString("#!/bin/sh\nprintf BUILD_OK\n"); err != nil {
		t.Fatal(err)
	}
	if err := tool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	home, root, hostSecret := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "secret")
	mustWriteFile(t, hostSecret, "host-only")
	t.Setenv("HOME", home)
	t.Setenv(sandbox.AllowUnsandboxedEnv, "0")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(bin, "fixture-tmp")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "Makefile"), "all:\n\t@test ! -e "+hostSecret+" && fixture-tmp && cat /tmp/private-marker\n")
	caller := sandboxCaller(t, root, "make", "fixture-tmp")
	result, err := runLine(t, caller, "printf PRIVATE_TMP > /tmp/private-marker")
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("seed private tmp: %+v %v", result, err)
	}
	result, err = runLine(t, caller, "make")
	if err != nil || result.ExitCode != 0 || result.Stdout != "BUILD_OKPRIVATE_TMP" {
		t.Fatalf("host /tmp exposed or private /tmp replaced: %+v %v", result, err)
	}
}
