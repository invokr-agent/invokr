package shell

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
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

	dirs := gitDirs(workspace)
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
	if dirs := gitDirs(root); dirs != nil {
		t.Fatalf("plain clone mounted %v", dirs)
	}
	if dirs := gitDirs(t.TempDir()); dirs != nil {
		t.Fatalf("workspace with no repository mounted %v", dirs)
	}
	if dirs := gitDirs(""); dirs != nil {
		t.Fatalf("empty workspace mounted %v", dirs)
	}
}

// A submodule's .git points straight at <super>/.git/modules/<name>, with no
// shared repository two levels up. The pointer itself still gets mounted.
func TestGitDirsMountsAPointerWithNoSharedRepo(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+target), 0o644); err != nil {
		t.Fatalf("write pointer: %v", err)
	}

	if dirs := gitDirs(root); !slices.Equal(dirs, []string{target}) {
		t.Fatalf("gitDirs = %v, want only %q", dirs, target)
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
