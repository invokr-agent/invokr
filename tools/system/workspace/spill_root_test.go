package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

// The policy comes from the app config's real defaults, so the spill root is
// registered the way the runtime registers it rather than by the test asserting
// on a policy it built itself.
func TestSpillCopyWriteRefusal(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	app := config.AppConfig{Workspace: config.WorkspaceConfig{Root: root}}
	policy := NewWorkspacePolicy(app.WithDefaults().Workspace)
	target := filepath.Join(home, ".invokr", "spill", "f1", "share.go")
	if _, err := policy.Resolve(target); err != nil {
		t.Fatalf("reading a spill copy refused: %v", err)
	}
	_, err := policy.ResolveWrite(target)
	if err == nil || !strings.Contains(err.Error(), "spilled tool-result copy") {
		t.Fatalf("err = %v, want the spill refusal naming the copy", err)
	}
	if _, ok := policy.ReadOnlyRoot(target); !ok {
		t.Fatalf("the spill root is writable: %v", err)
	}
}
