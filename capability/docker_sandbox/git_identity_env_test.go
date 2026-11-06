package docker_sandbox

import (
	"os"
	"slices"
	"testing"
)

func TestFilterEnvCarriesGitIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "octocat-bot")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_SSH_COMMAND", "/host/bin/ssh")
	env := filterEnv(os.Environ())
	for _, entry := range []string{"GIT_AUTHOR_NAME=octocat-bot", "GIT_CONFIG_KEY_0=credential.helper"} {
		if !slices.Contains(env, entry) {
			t.Errorf("expected %s in the relaunch environment: %v", entry, env)
		}
	}
	if slices.Contains(env, "GIT_SSH_COMMAND=/host/bin/ssh") {
		t.Errorf("GIT_SSH_COMMAND names a host path and must not travel into the container")
	}
}
