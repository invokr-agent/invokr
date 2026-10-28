package workspace

import (
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

// A model probing absolute paths was never told where it actually was and
// spent turns guessing, so the refusal names the root it should have used.
func TestResolveErrorNamesTheWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	_, err := policy.Resolve("/etc/passwd")
	if err == nil {
		t.Fatal("expected a path outside the workspace to be refused")
	}
	if !strings.Contains(err.Error(), "outside allowed workspace roots") {
		t.Fatalf("expected the existing wording kept, got %v", err)
	}
	if !strings.Contains(err.Error(), root) {
		t.Fatalf("expected the root %q named in %v", root, err)
	}
}
