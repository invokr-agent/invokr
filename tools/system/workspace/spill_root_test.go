package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
)

// A spill file is a saved copy of a tool result, not a file the workspace
// holds: an edit to one used to succeed while the source stayed unchanged
// (#92). Reading it stays allowed so a paged result remains reachable, and
// the refusal names the path so the model can write the real file.
func TestSpillCopiesRefuseWritesButStayReadable(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	spill := filepath.Join(home, constants.SpillDirName)
	if err := os.MkdirAll(filepath.Join(spill, "f1"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(spill, "f1", "share.go")
	if err := os.WriteFile(target, []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root, ExtraRoots: []string{spill}, ReadonlyRoots: []string{spill}})
	if _, err := policy.Resolve(target); err != nil {
		t.Fatalf("reading a spill copy refused: %v", err)
	}
	_, err := policy.ResolveWrite(target)
	if err == nil || !strings.Contains(err.Error(), target) {
		t.Fatalf("err = %v, want a refusal naming %s", err, target)
	}
	if _, ok := policy.ReadOnlyRoot(target); !ok {
		t.Fatalf("the spill root is writable: %v", err)
	}
}
