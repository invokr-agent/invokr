package shell

import (
	"os"
	"slices"
	"testing"
)

func TestIsolatedEnvCarriesGitIdentity(t *testing.T) {
	os.Setenv("GIT_AUTHOR_NAME", "octocat-bot")
	os.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	os.Setenv("GIT_DIR", "/tmp/invokr-identity-must-not-travel")
	defer func() {
		os.Unsetenv("GIT_AUTHOR_NAME")
		os.Unsetenv("GIT_CONFIG_KEY_0")
		os.Unsetenv("GIT_DIR")
	}()
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
