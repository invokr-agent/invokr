package shell

import (
	"slices"
	"testing"
)

func TestIsolatedEnvCarriesGitIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "octocat-bot")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_DIR", "/tmp/invokr-identity-must-not-travel")
	env := IsolatedEnv()
	for _, entry := range []string{"GIT_AUTHOR_NAME=octocat-bot", "GIT_CONFIG_KEY_0=credential.helper"} {
		if !slices.Contains(env, entry) {
			t.Errorf("expected %s in the isolated environment: %v", entry, env)
		}
	}
	if slices.Contains(env, "GIT_DIR=/tmp/invokr-identity-must-not-travel") {
		t.Errorf("GIT_DIR must stay out of an isolated command's environment")
	}
}
