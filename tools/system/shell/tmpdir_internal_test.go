package shell

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/fsutil"
)

// The sandbox's /tmp is a workspace directory, so a scratch file survives the
// call and code_interpreter reaches the same file under the same /tmp path.
func TestSharedTmpDirIsCreated(t *testing.T) {
	root := t.TempDir()
	dir := sharedTmpDir(root)
	want := fsutil.SharedTmpDir(root)
	if dir != want {
		t.Fatalf("expected %q, got %q", want, dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected the shared tmp dir to exist: %v", err)
	}
}

// The scratch dir lives outside the workspace, so it never turns up in the
// task repo's git status or in a directory listing the model reads.
func TestSharedTmpDirIsOutsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	if _, inside := fsutil.RelInside(root, sharedTmpDir(root)); inside {
		t.Fatalf("the shared tmp dir must not live inside the workspace: %s", sharedTmpDir(root))
	}
}

// With no workspace there is nothing to share, and the sandbox keeps its own
// private tmpfs rather than mounting an arbitrary host path at /tmp.
func TestSharedTmpDirEmptyWithoutWorkspace(t *testing.T) {
	if dir := sharedTmpDir(""); dir != "" {
		t.Fatalf("expected no shared tmp dir, got %q", dir)
	}
}

// The plan carries it, which is what makes the mount happen.
func TestSandboxPlanCarriesTheSharedTmpDir(t *testing.T) {
	root := t.TempDir()
	sb := sandboxPlan("/bin/bash", preparedShellCommand{workspaceRoot: root, cwd: root, line: "true", commands: []string{"true"}})
	if sb.TmpDir != fsutil.SharedTmpDir(root) {
		t.Fatalf("expected the shared tmp dir in the plan, got %q", sb.TmpDir)
	}
}

// The sandbox PATH keeps the user's bin dirs even when the agent process's own
// PATH is missing them, and the binaries there are mounted into the sandbox.
func TestSandboxEnvKeepsUserBinDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "faketool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin")

	if dirs := commandDirs("faketool"); len(dirs) == 0 {
		t.Fatal("a tool in ~/.local/bin must resolve for mounting")
	}
	plan := preparedShellCommand{commands: []string{"faketool"}, workspaceRoot: t.TempDir(), cwd: t.TempDir(), isolateEnv: true}
	sb := sandboxPlan("/usr/bin/bash", plan)
	if !slices.Contains(sb.RO, filepath.Join(home, ".local")) {
		t.Fatalf("user bin prefix not mounted: %v", sb.RO)
	}
	var path string
	for _, kv := range sandboxEnv(plan, sb) {
		if strings.HasPrefix(kv, "PATH=") {
			path = kv
		}
	}
	if !slices.Contains(filepath.SplitList(strings.TrimPrefix(path, "PATH=")), binDir) {
		t.Fatalf("%s missing from sandbox %s", binDir, path)
	}
}

// A read-only root is mounted read-only, so a write-tier line can read it but
// not write into it.
func TestSandboxPlanMountsReadonlyRootsReadOnly(t *testing.T) {
	ref := t.TempDir()
	plan := preparedShellCommand{workspaceRoot: t.TempDir(), cwd: t.TempDir(), readonlyRoots: []string{ref}}
	sb := sandboxPlan("/usr/bin/bash", plan)
	if !slices.Contains(sb.RO, ref) {
		t.Fatalf("read-only root not mounted read-only: %v", sb.RO)
	}
	if slices.Contains(sb.RW, ref) {
		t.Fatalf("read-only root must not be writable: %v", sb.RW)
	}
}

// A tool reached through a symlink keeps every hop of the chain mounted: the
// link in ~/.local/bin dangles inside the sandbox unless the directory holding
// its target is mounted too, and the runtime beside that target is what the
// tool actually needs.
func TestSandboxPlanMountsSymlinkedToolChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, ".local", "bin")
	toolBin := filepath.Join(home, "tool", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(toolBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolBin, "npm"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "tool", "bin", "npm"), filepath.Join(binDir, "npm")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin")

	plan := preparedShellCommand{commands: []string{"npm"}, workspaceRoot: t.TempDir(), cwd: t.TempDir(), isolateEnv: true}
	sb := sandboxPlan("/usr/bin/bash", plan)
	for _, want := range []string{filepath.Join(home, ".local"), filepath.Join(home, "tool")} {
		if !slices.Contains(sb.RO, want) {
			t.Fatalf("%s not mounted: %v", want, sb.RO)
		}
	}
	var path string
	for _, kv := range sandboxEnv(plan, sb) {
		if strings.HasPrefix(kv, "PATH=") {
			path = kv
		}
	}
	if !slices.Contains(filepath.SplitList(strings.TrimPrefix(path, "PATH=")), binDir) {
		t.Fatalf("%s missing from sandbox %s", binDir, path)
	}
}
