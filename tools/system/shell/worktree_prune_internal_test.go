package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
)

// Sibling worktrees are not mounted, so inside the sandbox they look deleted;
// a prune there must not destroy their entries in the shared repository.
func TestSandboxedPruneKeepsUnmountedSiblingWorktrees(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	main, sibling := t.TempDir(), filepath.Join(t.TempDir(), "wt")
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x"},
		{"worktree", "add", "-q", sibling},
	} {
		if out, err := exec.Command("git", append([]string{"-C", main}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	plan := preparedShellCommand{line: "git worktree prune", cwd: main, workspaceRoot: main, roots: []string{main}, commands: []string{"git"}, mode: approval.Auto}
	spec, err := processSpec(plan)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = core.RunProcess(context.Background(), spec)
	if _, err := os.Stat(filepath.Join(main, ".git", "worktrees", "wt")); err != nil {
		t.Fatalf("sandboxed prune deleted the sibling worktree entry: %v", err)
	}
}
