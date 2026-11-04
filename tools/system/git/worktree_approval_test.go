package git_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core"
)

// Writes are auto-approved in Auto, so any prompt here is the seal question.
func TestGitWriteInAWorktreeAsksOnce(t *testing.T) {
	repo := newRepo(t)
	worktree := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", worktree)
	if err := os.Mkdir(filepath.Join(worktree, ".invokr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &recordingApprover{approve: true}
	ctx, done := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "git_write"}, a)
	defer done()
	_, err := newCaller(t, worktree).HandleCall(ctx, &core.ToolCall{
		Name:      "git_write",
		Arguments: json.RawMessage(`{"op":"add","paths":["staged.txt"]}`),
	})
	if len(a.asked) != 0 {
		t.Fatalf("prompts = %d, want none: %s", len(a.asked), a.asked[0].Summary)
	}
	if err != nil {
		t.Fatalf("add in a worktree: %v", err)
	}
	if out := gitIn(t, worktree, "diff", "--cached", "--name-only"); out != "staged.txt" {
		t.Fatalf("staged = %q", out)
	}
}
