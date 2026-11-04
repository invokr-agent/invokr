package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

// linkedWorktree builds the layout git uses for `git worktree add`: the
// worktree's .git is a pointer file, and the shared repository sits outside it.
func linkedWorktree(t *testing.T) (main, worktree string) {
	t.Helper()
	main, worktree = t.TempDir(), filepath.Join(t.TempDir(), "wt")
	common, linked := filepath.Join(main, ".git"), filepath.Join(main, ".git", "worktrees", "wt")
	for _, dir := range []string{common, linked, worktree} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for file, content := range map[string]string{
		filepath.Join(worktree, ".git"): "gitdir: " + linked + "\n",
		filepath.Join(linked, "gitdir"): filepath.Join(worktree, ".git") + "\n",
	} {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return main, worktree
}

// A linked worktree reads hooks and shared config from the shared repository
// and has neither in its own git dir; sealing names the layout never creates
// asked for consent on every sandboxed write (#91).
func TestProtectedPathsFollowTheWorktreeLayout(t *testing.T) {
	main, worktree := linkedWorktree(t)
	paths := NewWorkspacePolicy(config.WorkspaceConfig{Root: worktree}).ProtectedPaths()
	for _, want := range []string{
		filepath.Join(main, ".git", "hooks"), filepath.Join(main, ".git", "config"), filepath.Join(worktree, ".git"),
	} {
		if !slices.Contains(paths, want) {
			t.Fatalf("protected set misses %s: %v", want, paths)
		}
	}
	for _, name := range []string{"hooks", "config"} {
		bogus := filepath.Join(main, ".git", "worktrees", "wt", name)
		if slices.Contains(paths, bogus) {
			t.Fatalf("protected set seals a name this layout never has: %s", bogus)
		}
	}
}

// A plain clone keeps .git inside the workspace; a workspace with no repository
// still consents to one a sandboxed command might create.
func TestProtectedPathsSealAPlainCloneAndAnAbsentRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths := NewWorkspacePolicy(config.WorkspaceConfig{Root: root}).ProtectedPaths()
	for _, want := range []string{filepath.Join(root, ".git", "hooks"), filepath.Join(root, ".git", "config")} {
		if !slices.Contains(paths, want) {
			t.Fatalf("plain clone misses %s: %v", want, paths)
		}
	}

	empty := t.TempDir()
	sealed := NewWorkspacePolicy(config.WorkspaceConfig{Root: empty}).ProtectedPaths()
	if !slices.Contains(sealed, filepath.Join(empty, ".git")) {
		t.Fatalf("an absent repository is not sealed: %v", sealed)
	}
	if slices.Contains(sealed, filepath.Join(empty, ".git", "hooks")) {
		t.Fatalf("an absent repository seals a git dir it does not have: %v", sealed)
	}
}
