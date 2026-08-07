package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// initRepo makes a repository with no user identity configured.
func initRepo(t *testing.T) string {
	t.Helper()
	// Isolate from the host's own git identity: without this the helper finds
	// the developer's global config and correctly declines to supply one.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "absent"))
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "--local", "--unset-all", "user.email"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		// The unset fails when nothing is set, which is the state we want anyway.
		_ = cmd.Run()
	}
	return root
}

// A repository with no identity gets one per invocation, so the model never
// needs `git config` — which would write the shared config and rewrite the
// identity for every linked worktree (#498).
func TestCommitIdentityEnvFillsAMissingIdentity(t *testing.T) {
	env := commitIdentityEnv(context.Background(), "commit", initRepo(t))
	if env == nil {
		t.Fatal("no identity supplied for a repository that has none")
	}
	for _, want := range []string{
		"GIT_AUTHOR_NAME=" + fallbackIdentityName,
		"GIT_AUTHOR_EMAIL=" + fallbackIdentityEmail,
		"GIT_COMMITTER_NAME=" + fallbackIdentityName,
		"GIT_COMMITTER_EMAIL=" + fallbackIdentityEmail,
	} {
		if !slices.Contains(env, want) {
			t.Fatalf("env missing %q", want)
		}
	}
}

// A configured identity is the user's, and is left alone.
func TestCommitIdentityEnvLeavesAConfiguredIdentityAlone(t *testing.T) {
	root := initRepo(t)
	cmd := exec.Command("git", "config", "--local", "user.email", "someone@example.com")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config: %v", err)
	}

	if env := commitIdentityEnv(context.Background(), "commit", root); env != nil {
		t.Fatalf("overrode a configured identity: %v", env)
	}
}

// Only commit needs an author; every other op runs with the inherited env.
func TestCommitIdentityEnvOnlyAppliesToCommit(t *testing.T) {
	root := initRepo(t)
	for _, op := range []string{"add", "branch_create", "checkout", "stash"} {
		if env := commitIdentityEnv(context.Background(), op, root); env != nil {
			t.Fatalf("op %q supplied an identity", op)
		}
	}
}
