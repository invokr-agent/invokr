package docker_sandbox

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/fsutil"
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

// The container starts with the host's GIT_CONFIG_* values, which name paths that
// do not exist inside it. Signing survives because main.go re-applies the stored
// identity against config.InvokrHomeDir(), and buildDockerArgs pins INVOKR_HOME to
// containerInvokrHome, where the host's Invokr home is bind-mounted. The re-applied
// user.signingkey replaces the forwarded one, so no per-key mount is needed (#103).
func TestContainerReappliesIdentityOverTheForwardedKey(t *testing.T) {
	// mountedHome stands in for containerInvokrHome, which is not writable here.
	hostHome, mountedHome := t.TempDir(), t.TempDir()
	for _, home := range []string{hostHome, mountedHome} {
		if err := fsutil.SaveJSON(config.GitIdentityPath(home), config.GitIdentityConfig{
			Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: config.GitIdentityKeyFileName}); err != nil {
			t.Fatalf("save identity under %s: %v", home, err)
		}
	}
	if _, err := config.ApplyGitIdentity(hostHome); err != nil {
		t.Fatalf("host apply: %v", err)
	}
	forwarded := filterEnv(os.Environ())
	if !slices.Contains(forwarded, "GIT_CONFIG_VALUE_4="+config.GitIdentityKeyPath(hostHome)) {
		t.Fatalf("the host signing key must travel into the container: %v", forwarded)
	}
	if _, err := config.ApplyGitIdentity(mountedHome); err != nil {
		t.Fatalf("container apply: %v", err)
	}
	if got := os.Getenv("GIT_CONFIG_VALUE_4"); got != config.GitIdentityKeyPath(mountedHome) {
		t.Errorf("the container kept %q, want %s", got, config.GitIdentityKeyPath(mountedHome))
	}
}

// The applied key path is derived from the home in use, so the copy inside the
// mount is what git signs with; the relaunch mounts the whole home there and adds
// no key-specific volume.
func TestContainerSigningKeyResolvesInsideTheMount(t *testing.T) {
	env := config.GitIdentityEnv(config.GitIdentityConfig{
		Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: config.GitIdentityKeyFileName}, containerInvokrHome)
	want := "GIT_CONFIG_VALUE_4=" + config.GitIdentityKeyPath(containerInvokrHome)
	if !slices.Contains(env, want) {
		t.Fatalf("missing %s in %v", want, env)
	}
	argv := buildDockerArgs("docker", "img", "/work", "/home/u/.invokr", nil, false, false, "")
	if key := config.GitIdentityKeyPath(containerInvokrHome); slices.ContainsFunc(argv, func(a string) bool {
		return strings.Contains(a, key)
	}) {
		t.Errorf("the key needs no mount of its own: %v", argv)
	}
	if !slices.Contains(argv, "/home/u/.invokr:"+containerInvokrHome) {
		t.Errorf("the Invokr home must be mounted at %s: %v", containerInvokrHome, argv)
	}
}
