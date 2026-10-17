package shell

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A user-installed tool is usually a symlink from ~/.local/bin into the
// manager's own tree. Running it by its absolute path must mount the tree the
// link points at, or the sandbox sees a dangling link.
func TestCommandDirsFollowsAnAbsolutePathThroughItsSymlink(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "share", "uv", "tools", "pytest", "bin")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	real := filepath.Join(realDir, "pytest")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	link := filepath.Join(binDir, "pytest")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dirs := commandDirs(link)
	if !slices.Contains(dirs, filepath.Join(root, "share", "uv", "tools", "pytest")) {
		t.Fatalf("commandDirs(%q) = %v; the symlink target's prefix is not mounted", link, dirs)
	}
}

func TestCommandDirsIgnoresRelativeAndMissingPaths(t *testing.T) {
	if dirs := commandDirs("./run.sh"); dirs != nil {
		t.Fatalf("relative path mounted %v", dirs)
	}
	if dirs := commandDirs(filepath.Join(t.TempDir(), "nope")); dirs != nil {
		t.Fatalf("missing path mounted %v", dirs)
	}
}
