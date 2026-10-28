package git_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core"
)

type recordingApprover struct {
	approve bool
	asked   []approval.Request
}

func (a *recordingApprover) RequestApproval(_ context.Context, req approval.Request) (approval.Decision, error) {
	a.asked = append(a.asked, req)
	return approval.Decision{Approved: a.approve, Reason: "no"}, nil
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repoWithRemote is newRepo on a feature branch with a commit, plus a bare
// file:// remote named origin.
func repoWithRemote(t *testing.T) (repo, bare string) {
	t.Helper()
	repo, bare = newRepo(t), filepath.Join(t.TempDir(), "remote.git")
	gitIn(t, repo, "init", "--bare", "-b", "main", bare)
	gitIn(t, repo, "remote", "add", "origin", bare)
	gitIn(t, repo, "checkout", "-b", "feat")
	gitIn(t, repo, "commit", "-am", "feature work")
	return repo, bare
}

func pushCall(t *testing.T, repo string, mode approval.Mode, a *recordingApprover, args string) (string, error) {
	t.Helper()
	ctx, done := approval.NewPolicy(mode, false).Begin(context.Background(), approval.Request{Tool: "git_write"}, a)
	defer done()
	return newCaller(t, repo).HandleCall(ctx, &core.ToolCall{Name: "git_write", Arguments: json.RawMessage(args)})
}

func TestGitWritePushesTheCurrentBranchAfterApproval(t *testing.T) {
	repo, bare := repoWithRemote(t)
	a := &recordingApprover{approve: true}
	if _, err := pushCall(t, repo, approval.Manual, a, `{"op":"push"}`); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(a.asked) != 1 {
		t.Fatalf("prompts = %d, want 1", len(a.asked))
	}
	req := a.asked[0]
	if req.Subject != "push origin feat" || req.GrantPattern != "push origin feat" || !req.Publishes || !req.Network || req.Confined ||
		!strings.Contains(req.Details, "feature work") || !strings.Contains(req.Details, "new branch") {
		t.Fatalf("request = %+v", req)
	}
	if got := gitIn(t, bare, "log", "--format=%s", "-1", "feat"); got != "feature work" {
		t.Fatalf("remote feat = %q", got)
	}
	if got := gitIn(t, repo, "config", "--get", "branch.feat.remote"); got != "origin" {
		t.Fatalf("upstream remote = %q, want origin (first push sets -u)", got)
	}
	// With an upstream, a second push lists only the new commits and no -u.
	gitIn(t, repo, "commit", "--allow-empty", "-m", "second")
	a.asked = nil
	if _, err := pushCall(t, repo, approval.Manual, a, `{"op":"push"}`); err != nil {
		t.Fatalf("second push: %v", err)
	}
	if d := a.asked[0].Details; strings.Contains(d, " -u ") || !strings.Contains(d, "second") || strings.Contains(d, "feature work") {
		t.Fatalf("second push details = %q", d)
	}
}

func TestGitWritePushRefusals(t *testing.T) {
	repo, bare := repoWithRemote(t)
	for args, want := range map[string]string{
		`{"op":"push","remote":"https://example.invalid/x.git"}`: "not a configured remote",
		`{"op":"push","remote":"upstream"}`:                      "not a configured remote",
	} {
		a := &recordingApprover{approve: true}
		if _, err := pushCall(t, repo, approval.Manual, a, args); err == nil || !strings.Contains(err.Error(), want) || len(a.asked) != 0 {
			t.Fatalf("%s: err=%v prompts=%d, want %q before any prompt", args, err, len(a.asked), want)
		}
	}
	a := &recordingApprover{}
	_, err := pushCall(t, repo, approval.Manual, a, `{"op":"push","force_with_lease":true}`)
	var denied *approval.DeniedError
	if !errors.As(err, &denied) || a.asked[0].GrantPattern != "" || !strings.Contains(a.asked[0].Details, "FORCE PUSH") {
		t.Fatalf("denied force push: err=%v request=%+v", err, a.asked)
	}
	if out := gitIn(t, bare, "branch", "--list", "feat"); out != "" {
		t.Fatalf("denied push still reached the remote: %q", out)
	}
	if _, err := pushCall(t, repo, approval.Plan, &recordingApprover{approve: true}, `{"op":"push"}`); err == nil {
		t.Fatal("plan mode pushed")
	}
	gitIn(t, repo, "checkout", "--detach")
	if _, err := pushCall(t, repo, approval.Manual, &recordingApprover{approve: true}, `{"op":"push"}`); err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("detached HEAD: err=%v", err)
	}
}

// TestGitWritePushAsksOnceInAuto covers the read-only probes preparePush runs
// before approval: they mount nothing writable, so Auto mode raises exactly the
// one approval for the push itself, never a seal prompt per probe.
func TestGitWritePushAsksOnceInAuto(t *testing.T) {
	repo, bare := repoWithRemote(t)
	a := &recordingApprover{approve: true}
	if _, err := pushCall(t, repo, approval.Auto, a, `{"op":"push"}`); err != nil {
		t.Fatalf("auto push: %v", err)
	}
	if len(a.asked) != 1 {
		t.Fatalf("prompts = %d, want 1 (the push)", len(a.asked))
	}
	if req := a.asked[0]; req.Subject != "push origin feat" || !req.Publishes || !req.Network {
		t.Fatalf("prompt = %+v, want the push itself", a.asked[0])
	}
	if got := gitIn(t, bare, "log", "--format=%s", "-1", "feat"); got != "feature work" {
		t.Fatalf("remote feat = %q", got)
	}
}

// TestGitWritePushReportsProbeFailure pins the rev-parse failure to its own
// cause: only a literal HEAD is a detached checkout, so a directory that is not
// a repository must not be reported as one.
func TestGitWritePushReportsProbeFailure(t *testing.T) {
	_, err := pushCall(t, t.TempDir(), approval.Manual, &recordingApprover{approve: true}, `{"op":"push"}`)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") || strings.Contains(err.Error(), "detached") {
		t.Fatalf("not a repo: err=%v, want the rev-parse cause", err)
	}
}

func TestGitWritePushWarnsAboutWorkspaceHooks(t *testing.T) {
	repo, _ := repoWithRemote(t)
	gitIn(t, repo, "config", "core.hooksPath", ".githooks")
	for args, want := range map[string]bool{`{"op":"push"}`: true, `{"op":"push","no_verify":true}`: false} {
		a := &recordingApprover{}
		_, _ = pushCall(t, repo, approval.Manual, a, args)
		if got := strings.Contains(a.asked[0].Details, "workspace-writable"); got != want {
			t.Fatalf("%s: hook warning = %v, want %v (%q)", args, got, want, a.asked[0].Details)
		}
	}
}
