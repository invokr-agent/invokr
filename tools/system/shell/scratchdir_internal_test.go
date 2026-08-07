package shell

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

func TestShellCWDOutsideTheWorkspaceNamesTheScratchDir(t *testing.T) {
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	_, err := resolveShellCWD("/tmp/vktest", wsp)
	if err == nil {
		t.Fatalf("expected /tmp/vktest to be refused as a cwd")
	}
	for _, want := range []string{"/tmp", "code_interpreter", "cd /tmp"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q does not mention %q", err.Error(), want)
		}
	}
}

// A cwd inside the workspace that simply does not exist keeps the listing hint.
func TestShellCWDMissingInsideTheWorkspaceKeepsItsListing(t *testing.T) {
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	_, err := resolveShellCWD("nope", wsp)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want the missing-directory message", err)
	}
	if strings.Contains(err.Error(), "code_interpreter") {
		t.Fatalf("scratch hint leaked onto an in-workspace path: %v", err)
	}
}
