package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

func TestResolveRejectsNewFileThroughOutsideSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	if _, err := policy.Resolve("outside-link/new.txt"); err == nil {
		t.Fatal("expected outside symlink rejection")
	}
}

func TestResolveRejectsBrokenFileSymlinkOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "new.txt"), filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	if _, err := policy.Resolve("outside-link"); err == nil {
		t.Fatal("expected broken outside symlink rejection")
	}
}

func TestResolveRejectsTooManySymlinks(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 41; i++ {
		if err := os.Symlink(fmt.Sprintf("link-%d", i+1), filepath.Join(root, fmt.Sprintf("link-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	if _, err := policy.Resolve("link-0"); err == nil {
		t.Fatal("expected excessive symlink traversal rejection")
	}
}

func TestResolveAllowsDeepNewPath(t *testing.T) {
	root := t.TempDir()
	path := strings.Repeat("dir/", 50) + "new.txt"
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	if _, err := policy.Resolve(path); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAllowsNewFileThroughInternalSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "internal-link")); err != nil {
		t.Fatal(err)
	}
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	got, err := policy.Resolve("internal-link/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(target, "new.txt"); got != want {
		t.Fatalf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveRejectsBrokenSymlinkOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "new.txt")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	if _, err := policy.Resolve("link"); err == nil {
		t.Fatal("expected broken outside symlink rejection")
	}
}

// A read-only root is readable like any extra root, but every write path
// resolution through it is refused with a message naming the root.
func TestReadonlyRootsReadableNotWritable(t *testing.T) {
	root := t.TempDir()
	ref := t.TempDir()
	file := filepath.Join(ref, "notes.md")
	if err := os.WriteFile(file, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewWorkspacePolicy(config.WorkspaceConfig{Root: root, ReadonlyRoots: []string{ref}})

	if _, err := p.Resolve(file); err != nil {
		t.Fatalf("read must be allowed: %v", err)
	}
	if _, err := p.ResolveWrite(filepath.Join(ref, "site", "new.txt")); err == nil {
		t.Fatal("write under a read-only root must be refused")
	} else if !strings.Contains(err.Error(), "is writable") {
		t.Fatalf("refusal must name the writable workspace root, got %v", err)
	} else if !strings.Contains(err.Error(), "read-only root") {
		t.Fatalf("unhelpful message: %v", err)
	}
	if _, err := p.ResolveWrite(filepath.Join(root, "ok.txt")); err != nil {
		t.Fatalf("write inside the workspace must stay allowed: %v", err)
	}
	if got := p.WritableRoots(); len(got) != 1 || got[0] != filepath.Clean(root) {
		t.Fatalf("writable roots = %v", got)
	}
	if got := p.ReadOnlyRoots(); len(got) != 1 || got[0] != filepath.Clean(ref) {
		t.Fatalf("read-only roots = %v", got)
	}
}

// An absolute path the model invented from a container-style mount point
// ("/app/config.py") resolves against the workspace root when that file
// exists there, instead of being refused as outside the roots (r10 L1/1).
func TestResolveRebasesAbsolutePathOntoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "app", "config.py")
	if err := os.WriteFile(target, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})

	resolved, err := policy.Resolve("/app/config.py")
	if err != nil {
		t.Fatalf("resolve rebased path: %v", err)
	}
	if got, want := resolved, mustEval(t, target); got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}

	// Nothing at <root>/etc/shadow, so the refusal stands.
	if _, err := policy.Resolve("/etc/shadow"); err == nil {
		t.Fatal("an absolute path with no counterpart in the workspace must still be refused")
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestGitignoreIsNotHidden(t *testing.T) {
	p := Policy{}
	for path, want := range map[string]bool{".gitignore": true, "sub/.gitignore": true, ".git/info/exclude": false, ".env": false, ".hidden/.gitignore": false} {
		if got := p.AllowsHiddenPath(path); got != want {
			t.Fatalf("AllowsHiddenPath(%q) = %v, want %v", path, got, want)
		}
	}
}
