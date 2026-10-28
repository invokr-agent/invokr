package shell

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
	workspacepolicy "github.com/invokr-agent/invokr/tools/system/workspace"
)

// A linked worktree's .git is a pointer file, and what it names lives outside
// the workspace. Both it and the shared repository must be mounted or git
// answers "not a git repository" for every command.
func TestGitDirsMountsAWorktreePointerAndItsSharedRepo(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, "main", ".git")
	linked := filepath.Join(gitDir, "worktrees", "wt")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	workspace := filepath.Join(root, "wt")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+linked+"\n"), 0o644); err != nil {
		t.Fatalf("write pointer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(linked, "gitdir"), []byte(filepath.Join(workspace, ".git")+"\n"), 0o644); err != nil {
		t.Fatalf("write reciprocal pointer: %v", err)
	}

	dirs := workspacepolicy.GitDirs(workspace)
	if !slices.Contains(dirs, linked) {
		t.Fatalf("gitDirs(%q) = %v; the worktree's own git dir is not mounted", workspace, dirs)
	}
	if !slices.Contains(dirs, gitDir) {
		t.Fatalf("gitDirs(%q) = %v; the shared repository is not mounted", workspace, dirs)
	}
}

// A plain clone keeps .git inside the workspace, which is already mounted.
func TestGitDirsIgnoresAPlainCloneAndAMissingRepo(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if dirs := workspacepolicy.GitDirs(root); dirs != nil {
		t.Fatalf("plain clone mounted %v", dirs)
	}
	if dirs := workspacepolicy.GitDirs(t.TempDir()); dirs != nil {
		t.Fatalf("workspace with no repository mounted %v", dirs)
	}
	if dirs := workspacepolicy.GitDirs(""); dirs != nil {
		t.Fatalf("empty workspace mounted %v", dirs)
	}
}

// An arbitrary workspace pointer does not grant a new writable host path.
func TestGitDirsRejectsUnrelatedPointer(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+target), 0o644); err != nil {
		t.Fatalf("write pointer: %v", err)
	}

	if dirs := workspacepolicy.GitDirs(root); len(dirs) != 0 {
		t.Fatalf("unrelated path mounted: %v", dirs)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ../elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirs := workspacepolicy.GitDirs(root); len(dirs) != 0 {
		t.Fatalf("relative traversal mounted unrelated path: %v", dirs)
	}
}

// An absorbed submodule has a reciprocal pointer but no shared worktree repo.
func TestGitDirsMountsReciprocalSubmodule(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "module")
	target := filepath.Join(root, ".git", "modules", "module")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+target), 0o644); err != nil {
		t.Fatal(err)
	}
	// Real git records the submodule back-pointer as core.worktree, not a gitdir file.
	if err := os.WriteFile(filepath.Join(target, "config"), []byte("[core]\n\tworktree = ../../../module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirs := workspacepolicy.GitDirs(workspace); !slices.Equal(dirs, []string{target}) {
		t.Fatalf("submodule git dir = %v, want %q", dirs, target)
	}
}

func TestGitDirsMountsQuotedSubmoduleWorktree(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "module#hash")
	target := filepath.Join(root, ".git", "modules", "module#hash")
	for _, dir := range []string{workspace, target} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+target), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config"), []byte("[core]\n\tworktree = \"../../../module#hash\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirs := workspacepolicy.GitDirs(workspace); !slices.Equal(dirs, []string{target}) {
		t.Fatalf("quoted submodule git dir = %v, want %q", dirs, target)
	}
}

func TestGitDirsRejectsChangedAndSymlinkedPointer(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "work")
	target := filepath.Join(root, ".git", "worktrees", "work")
	other := filepath.Join(root, "other")
	for _, dir := range []string{workspace, target, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pointer := filepath.Join(workspace, ".git")
	if err := os.WriteFile(pointer, []byte("gitdir: "+target), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "gitdir"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(workspacepolicy.GitDirs(workspace)) != 2 {
		t.Fatal("expected valid worktree before pointer replacement")
	}
	if err := os.WriteFile(pointer, []byte("gitdir: "+other), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirs := workspacepolicy.GitDirs(workspace); len(dirs) != 0 {
		t.Fatalf("changed pointer mounted unrelated path: %v", dirs)
	}
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "gitdir"), pointer); err != nil {
		t.Fatal(err)
	}
	if dirs := workspacepolicy.GitDirs(workspace); len(dirs) != 0 {
		t.Fatalf("symlinked pointer mounted: %v", dirs)
	}
}

func TestGitPointerCannotExpandSandboxWritableMounts(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	workspace, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+outside), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "escape")
	plan := preparedShellCommand{workspaceRoot: workspace, cwd: workspace, roots: []string{workspace},
		commands: []string{"git", "touch"}, line: "touch " + marker}
	assertNoWrite := func() {
		t.Helper()
		if slices.Contains(sandboxPlan("/bin/bash", plan).RW, outside) {
			t.Fatal("outside directory added to writable mounts")
		}
		spec, err := processSpec(plan)
		if err != nil {
			t.Fatal(err)
		}
		command, cleanup, err := spec.Build(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cleanup() }()
		command.Env = spec.Env
		if out, err := command.CombinedOutput(); err == nil {
			t.Fatalf("sandbox wrote through workspace .git pointer: %s", out)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("outside marker created: %v", err)
		}
	}
	assertNoWrite()
	if err := os.Remove(filepath.Join(workspace, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ".git")); err != nil {
		t.Fatal(err)
	}
	plan.mode = approval.Auto
	assertNoWrite()
}

func TestGitMountSourcePinnedAcrossPathReplacement(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root, outside := t.TempDir(), t.TempDir()
	workspace := filepath.Join(root, "wt")
	linked := filepath.Join(root, "main", ".git", "worktrees", "wt")
	for _, dir := range []string{workspace, linked} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pointer := filepath.Join(workspace, ".git")
	if err := os.WriteFile(pointer, []byte("gitdir: "+linked), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "gitdir"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Dir(filepath.Dir(linked))
	marker := filepath.Join(shared, "proof")
	plan := preparedShellCommand{workspaceRoot: workspace, cwd: workspace, roots: []string{workspace},
		commands: []string{"git", "touch"}, line: "touch " + marker}
	spec, err := processSpec(plan)
	if err != nil {
		t.Fatal(err)
	}
	command, cleanup, err := spec.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	if err := os.Rename(shared, shared+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, shared); err != nil {
		t.Fatal(err)
	}
	command.Env = spec.Env
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sandbox could not write pinned Git mount: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(shared+"-old", "proof")); err != nil {
		t.Fatalf("pinned Git directory did not receive write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "proof")); !os.IsNotExist(err) {
		t.Fatalf("outside directory received write: %v", err)
	}
}

func TestGitPinDescriptorClosedBeforePayload(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "wt")
	shared := filepath.Join(root, "main", ".git")
	linked := filepath.Join(shared, "worktrees", "wt")
	for _, dir := range []string{workspace, linked, filepath.Join(shared, "hooks"), filepath.Join(linked, "hooks")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pointer := filepath.Join(workspace, ".git")
	if err := os.WriteFile(pointer, []byte("gitdir: "+linked), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "gitdir"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{shared, linked} {
		if err := os.WriteFile(filepath.Join(dir, "config"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(shared, "hooks", "escape")
	for _, mode := range []approval.Mode{approval.Auto, approval.Plan} {
		t.Run(string(mode), func(t *testing.T) {
			plan := preparedShellCommand{workspaceRoot: workspace, cwd: workspace, roots: []string{workspace}, mode: mode,
				commands: []string{"git", "touch"}, line: "if test -d /proc/self/fd/3/hooks; then touch /proc/self/fd/3/hooks/escape; else echo closed; fi"}
			sb := sandboxPlan("/bin/bash", plan)
			if mode == approval.Plan && len(sb.PinnedRW) != 0 {
				t.Fatalf("Plan retains writable Git pins: %v", sb.PinnedRW)
			}
			spec, err := processSpec(plan)
			if err != nil {
				t.Fatal(err)
			}
			command, cleanup, err := spec.Build(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cleanup() }()
			command.Env = spec.Env
			if out, err := command.CombinedOutput(); err != nil || string(out) != "closed\n" {
				t.Fatalf("payload inherited Git pin: %v: %s", err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("payload wrote through Git pin: %v", err)
			}
		})
	}
}

// The home directory is not mounted, so without the global config git inside
// the sandbox has no author and cannot commit (#498).
func TestGitConfigFilesFindsAnExplicitGlobalConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\temail = a@b.c\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)

	if got := gitConfigFiles(); !slices.Contains(got, cfg) {
		t.Fatalf("gitConfigFiles() = %v; the explicit global config is not mounted", got)
	}
}

// A path that does not exist must not be mounted: the sandbox rejects a bind
// whose source is missing.
func TestGitConfigFilesSkipsMissingPaths(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "absent"))

	for _, path := range gitConfigFiles() {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("gitConfigFiles() returned a path that does not exist: %s", path)
		}
	}
}
