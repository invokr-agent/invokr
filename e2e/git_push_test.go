//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

// git_write op=push sends the workspace's current branch to its remote after
// one publishing approval, using host git rather than the shell sandbox.
func TestGitWritePushReachesTheRemote(t *testing.T) {
	home := newHome(t, "")
	bare := filepath.Join(t.TempDir(), "remote.git")
	git := func(dir string, args ...string) string {
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
	git(home.workspace, "init", "-b", "feat-push")
	git(home.workspace, "add", "README.md")
	git(home.workspace, "commit", "-m", "e2e push commit")
	git(home.workspace, "init", "--bare", bare)
	git(home.workspace, "remote", "add", "origin", bare)

	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("push-1", "git_write", map[string]any{"op": "push"}),
		providertest.ReplyStep("pushed"),
	)
	defer provider.Close()
	var mu sync.Mutex
	var asked []approval.Request
	got := runInProc(t, home, provider.URL(), "push the branch", func(req approval.Request) approval.Decision {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, req)
		return approval.Decision{Approved: true}
	})
	if got.Resp.Message != "pushed" {
		t.Fatalf("final response = %q", got.Resp.Message)
	}
	if len(asked) != 1 || asked[0].Subject != "push origin feat-push" || !asked[0].Publishes {
		t.Fatalf("approvals = %+v, want one publishing push request", asked)
	}
	if subject := git(bare, "log", "--format=%s", "-1", "feat-push"); subject != "e2e push commit" {
		t.Fatalf("remote branch = %q", subject)
	}
	provider.AssertClean(t)
}
