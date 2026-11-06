package config

import (
	"errors"
	"github.com/invokr-agent/invokr/support/fsutil"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testHomeDir = "/home/operator/.invokr"

// containerHomeDir is where docker_sandbox mounts the Invokr home inside the
// container; an identity applied there must name a key under it.
const containerHomeDir = "/root/.invokr"

func TestGitIdentityEnvEmpty(t *testing.T) {
	for _, identity := range []GitIdentityConfig{GitIdentityConfig{}, GitIdentityConfig{Name: "octocat-bot"}, GitIdentityConfig{Email: "octocat-bot@example.com"}} {
		if got := GitIdentityEnv(identity, testHomeDir); len(got) != 0 {
			t.Errorf("%v must set no variables, got %v", identity, got)
		}
	}
}

func TestGitIdentityEnvIdentity(t *testing.T) {
	env := GitIdentityEnv(GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com"}, testHomeDir)
	for _, entry := range []string{
		"GIT_AUTHOR_NAME=octocat-bot", "GIT_AUTHOR_EMAIL=octocat-bot@example.com",
		"GIT_COMMITTER_NAME=octocat-bot", "GIT_COMMITTER_EMAIL=octocat-bot@example.com",
		"GH_CONFIG_DIR=" + testHomeDir + "/gh", "GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.https://github.com.helper", "GIT_CONFIG_VALUE_1=!gh auth git-credential",
		"GIT_CONFIG_KEY_2=url.https://github.com/.insteadOf", "GIT_CONFIG_VALUE_2=git@github.com:",
	} {
		if !slices.Contains(env, entry) {
			t.Errorf("missing %s in %v", entry, env)
		}
	}
	if len(env) != 12 {
		t.Errorf("expected 12 entries without a signing key, got %d: %v", len(env), env)
	}
}

// The stored signing key is only the flag that this identity signs: the path git
// signs with is always derived from the home in use, so the same Invokr home
// mounted at another path (the container's /root/.invokr) resolves to the copy
// that exists there instead of a host path that dangles.
func TestGitIdentityEnvSigningDerivesKeyFromHome(t *testing.T) {
	for _, homeDir := range []string{testHomeDir, containerHomeDir} {
		for _, stored := range []string{GitIdentityKeyFileName, GitIdentityKeyPath(testHomeDir)} {
			env := GitIdentityEnv(GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: stored}, homeDir)
			for _, entry := range []string{
				"GIT_CONFIG_COUNT=6", "GIT_CONFIG_KEY_3=gpg.format", "GIT_CONFIG_VALUE_3=ssh",
				"GIT_CONFIG_KEY_4=user.signingkey", "GIT_CONFIG_VALUE_4=" + GitIdentityKeyPath(homeDir),
				"GIT_CONFIG_KEY_5=commit.gpgsign", "GIT_CONFIG_VALUE_5=true",
			} {
				if !slices.Contains(env, entry) {
					t.Errorf("home %s stored %q: missing %s in %v", homeDir, stored, entry, env)
				}
			}
			if len(env) != 18 {
				t.Errorf("home %s stored %q: expected 15 entries, got %d: %v", homeDir, stored, len(env), env)
			}
		}
	}
}

func TestGitIdentityEnvNamesCover(t *testing.T) {
	env := GitIdentityEnv(GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: "/tmp/k"}, testHomeDir)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(GitIdentityEnvNames(), key) {
			t.Errorf("%s is applied but not forwarded to a sandboxed command", key)
		}
	}
}

func TestGitIdentityStore(t *testing.T) {
	dir := t.TempDir()
	identity := GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: GitIdentityKeyFileName}
	if err := fsutil.SaveJSON(GitIdentityPath(dir), identity); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gh")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("saving must not create the gh dir: %v", err)
	}
	info, err := os.Stat(GitIdentityPath(dir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600, got %o", info.Mode().Perm())
	}
	loaded, err := LoadGitIdentity(dir)
	if err != nil || loaded.Name != identity.Name || loaded.Email != identity.Email || loaded.SigningKey != identity.SigningKey {
		t.Fatalf("round trip: %v (%v)", loaded, err)
	}
	if err := os.MkdirAll(GitIdentityDir(dir), 0o700); err != nil {
		t.Fatalf("mkdir gh: %v", err)
	}
	if err := os.WriteFile(GitIdentityKeyPath(dir), []byte("key material"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := ClearGitIdentity(dir); err != nil {
			t.Fatalf("clear (%d): %v", i, err)
		}
	}
	if _, err := os.Stat(GitIdentityDir(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clear must remove %s: %v", GitIdentityDir(dir), err)
	}
	if _, err := os.Stat(GitIdentityKeyPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clear must remove %s: %v", GitIdentityKeyPath(dir), err)
	}
	if loaded, err := LoadGitIdentity(dir); err != nil || loaded.Name != "" || loaded.Email != "" || loaded.SigningKey != "" {
		t.Errorf("after clear expected an empty identity, got %v (%v)", loaded, err)
	}
}

// --reset takes the copied signing key with the identity: a leftover key would
// keep signing commits no account asked for, and it must stay idempotent.
func TestClearGitIdentityRemovesTheKeyFile(t *testing.T) {
	dir := t.TempDir()
	key := GitIdentityKeyPath(dir)
	if err := os.WriteFile(key, []byte("key material"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := ClearGitIdentity(dir); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(key); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clear must remove the copied key: %v", err)
	}
	if err := ClearGitIdentity(dir); err != nil {
		t.Errorf("clear must stay idempotent: %v", err)
	}
}

// git derives the public half from the private key, so only the private key is
// copied: one file under the home is one file the sandbox can mount.
func TestGitIdentityKeyPathIsUnderTheHome(t *testing.T) {
	for _, homeDir := range []string{testHomeDir, containerHomeDir} {
		key := GitIdentityKeyPath(homeDir)
		if filepath.Dir(key) != homeDir || filepath.Base(key) != GitIdentityKeyFileName {
			t.Errorf("%s is not the key file directly under %s", key, homeDir)
		}
	}
}
