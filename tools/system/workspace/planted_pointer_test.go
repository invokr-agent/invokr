package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

// A planted .git pointer file that GitDirs will not verify must be sealed itself:
// sealing its children instead leaves the file writable, and a sandboxed command
// could repoint it at a directory full of hooks.
func TestUnverifiedGitPointerFileIsSealedItself(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /tmp/planted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	sealed := policy.ProtectedPaths()
	git := filepath.Join(root, ".git")
	if !slices.Contains(sealed, git) {
		t.Fatalf("the pointer file is writable: %v", sealed)
	}
	for _, path := range sealed {
		if path == filepath.Join(git, "hooks") || path == filepath.Join(git, "config") {
			t.Fatalf("a child of a file is sealed: %v", sealed)
		}
	}
}
