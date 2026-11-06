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

func TestGitIdentityEnvSigning(t *testing.T) {
	env := GitIdentityEnv(GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: "/home/operator/.ssh/id_ed25519_signer"}, testHomeDir)
	for _, entry := range []string{
		"GIT_CONFIG_COUNT=6", "GIT_CONFIG_KEY_3=gpg.format", "GIT_CONFIG_VALUE_3=ssh",
		"GIT_CONFIG_KEY_4=user.signingkey", "GIT_CONFIG_VALUE_4=/home/operator/.ssh/id_ed25519_signer",
		"GIT_CONFIG_KEY_5=commit.gpgsign", "GIT_CONFIG_VALUE_5=true",
	} {
		if !slices.Contains(env, entry) {
			t.Errorf("missing %s in %v", entry, env)
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
	identity := GitIdentityConfig{Name: "octocat-bot", Email: "octocat-bot@example.com", SigningKey: "/home/operator/.ssh/id_ed25519_signer"}
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
	for i := 0; i < 2; i++ {
		if err := ClearGitIdentity(dir); err != nil {
			t.Fatalf("clear (%d): %v", i, err)
		}
	}
	if _, err := os.Stat(GitIdentityDir(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clear must remove %s: %v", GitIdentityDir(dir), err)
	}
	if loaded, err := LoadGitIdentity(dir); err != nil || loaded.Name != "" || loaded.Email != "" || loaded.SigningKey != "" {
		t.Errorf("after clear expected an empty identity, got %v (%v)", loaded, err)
	}
}

// --reset takes the copied signing key with the identity: a leftover key would
// keep signing commits no account asked for.
func TestClearGitIdentityRemovesCopiedKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(GitIdentityKeyDir(dir), 0o700); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	key := filepath.Join(GitIdentityKeyDir(dir), "id_ed25519_signer")
	for _, path := range []string{key, key + ".pub"} {
		if err := os.WriteFile(path, []byte("key material"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := ClearGitIdentity(dir); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(GitIdentityKeyDir(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clear must remove the copied keys: %v", err)
	}
	if err := ClearGitIdentity(dir); err != nil {
		t.Errorf("clear must stay idempotent: %v", err)
	}
}

// A sandbox mounts exactly these two files: the private key git signs with and
// the public half it verifies against, and nothing at all when the identity does
// not sign.
func TestSigningKeyFilesNamesBothHalves(t *testing.T) {
	key := "/home/operator/.invokr/ssh/id_ed25519_signer"
	want := []string{key, key + ".pub"}
	if got := SigningKeyFiles(GitIdentityConfig{SigningKey: key}); !slices.Equal(got, want) {
		t.Fatalf("SigningKeyFiles = %v, want %v", got, want)
	}
	if got := SigningKeyFiles(GitIdentityConfig{Name: "octocat-bot"}); len(got) != 0 {
		t.Fatalf("an identity with no signing key mounted %v", got)
	}
}
