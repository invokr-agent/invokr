package git

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

// A repository that pins its own identity must still be committed as the
// configured invokr identity: that is what the environment override buys over
// a repo-level user.email, which would otherwise win.
func TestCommitIdentityEnvBeatsRepositoryIdentity(t *testing.T) {
	repo, ctx := t.TempDir(), context.Background()
	env := append(os.Environ(), config.GitIdentityEnv(config.GitIdentityConfig{
		Name: "octocat-bot", Email: "octocat-bot@example.com",
	}, repo)...)
	for _, argv := range [][]string{{"init"}, {"config", "user.email", "host-user@example.invalid"}, {"config", "user.name", "Host User"}} {
		if _, err := gitTextEnv(ctx, repo, env, argv...); err != nil {
			t.Fatalf("git %v: %v", argv, err)
		}
	}
	if _, err := gitTextEnv(ctx, repo, env, "commit", "--allow-empty", "-m", "identity"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	out, err := gitText(ctx, repo, "log", "-1", "--format=%an <%ae> %cn <%ce>")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	want := "octocat-bot <octocat-bot@example.com> octocat-bot <octocat-bot@example.com>"
	if strings.TrimSpace(out) != want {
		t.Errorf("expected %q, got %q", want, out)
	}
}
