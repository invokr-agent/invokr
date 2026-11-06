package setup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

func TestRunGitIdentityStepStoresAnswers(t *testing.T) {
	homeDir, dir := t.TempDir(), t.TempDir()
	answersPath, outPath := filepath.Join(dir, "answers"), filepath.Join(dir, "out")
	if err := os.WriteFile(answersPath, []byte("octocat-bot\noctocat-bot@example.com\n\n"), 0o600); err != nil {
		t.Fatalf("answers: %v", err)
	}
	in, err := os.Open(answersPath)
	if err != nil {
		t.Fatalf("open answers: %v", err)
	}
	t.Cleanup(func() { _ = in.Close() })
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create out: %v", err)
	}
	identity, err := RunGitIdentityStep(in, out, homeDir)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Errorf("close out: %v", err)
	}
	printed, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	for _, prompt := range []string{"Git name for invokr", "Git email for invokr", "SSH signing key"} {
		if !strings.Contains(string(printed), prompt) {
			t.Errorf("no %q prompt in %s", prompt, printed)
		}
	}
	stored, err := config.LoadGitIdentity(homeDir)
	if err != nil || stored.Name != "octocat-bot" || stored.Email != "octocat-bot@example.com" || stored.SigningKey != "" {
		t.Fatalf("stored identity is wrong: %v (%v)", stored, err)
	}
	if identity.Name != stored.Name {
		t.Errorf("returned %v, stored %v", identity, stored)
	}
}

func TestRunGitIdentityStepRefusesHalfIdentity(t *testing.T) {
	homeDir, dir := t.TempDir(), t.TempDir()
	answersPath, outPath := filepath.Join(dir, "answers"), filepath.Join(dir, "out")
	if err := os.WriteFile(answersPath, []byte("octocat-bot\n\n\n"), 0o600); err != nil {
		t.Fatalf("answers: %v", err)
	}
	in, err := os.Open(answersPath)
	if err != nil {
		t.Fatalf("open answers: %v", err)
	}
	t.Cleanup(func() { _ = in.Close() })
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create out: %v", err)
	}
	if _, err := RunGitIdentityStep(in, out, homeDir); err == nil {
		t.Errorf("a missing email must be refused, not stored")
	}
	stored, err := config.LoadGitIdentity(homeDir)
	if err != nil || stored.Name != "" {
		t.Errorf("nothing must be stored: %v (%v)", stored, err)
	}
}

// The answered key is copied to one fixed file under the Invokr home at 0600, and
// only the private half travels: git derives the public half from it. A sandboxed
// commit can only sign with a file the sandbox mounts, so the host's own ~/.ssh
// is never written and nothing else under the home is exposed. A wider copy left
// by an earlier run is tightened, not trusted.
func TestRunGitIdentityStepCopiesTheSigningKey(t *testing.T) {
	homeDir, dir := t.TempDir(), t.TempDir()
	host := filepath.Join(dir, "ssh")
	if err := os.MkdirAll(host, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	source := filepath.Join(host, "id_ed25519_signer")
	if err := os.WriteFile(source, []byte("key material\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(config.GitIdentityKeyPath(homeDir), []byte("stale"), 0o644); err != nil {
		t.Fatalf("widen an existing copy: %v", err)
	}
	answersPath, outPath := filepath.Join(dir, "answers"), filepath.Join(dir, "out")
	if err := os.WriteFile(answersPath, []byte("octocat-bot\noctocat-bot@example.com\n"+source+"\n"), 0o600); err != nil {
		t.Fatalf("answers: %v", err)
	}
	in, err := os.Open(answersPath)
	if err != nil {
		t.Fatalf("open answers: %v", err)
	}
	t.Cleanup(func() { _ = in.Close() })
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create out: %v", err)
	}
	if _, stepErr := RunGitIdentityStep(in, out, homeDir); stepErr != nil {
		t.Fatalf("step: %v", stepErr)
	}
	stored, err := config.LoadGitIdentity(homeDir)
	if err != nil || stored.SigningKey != config.GitIdentityKeyFileName {
		t.Fatalf("stored signing key = %q (%v), want the stored name %q", stored.SigningKey, err, config.GitIdentityKeyFileName)
	}
	key := config.GitIdentityKeyPath(homeDir)
	info, statErr := os.Stat(key)
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("copy %s: %v (%o)", key, statErr, info.Mode().Perm())
	}
	if data, readErr := os.ReadFile(key); readErr != nil || !slices.Equal(data, []byte("key material\n")) {
		t.Fatalf("copy %s content: %v", key, readErr)
	}
	if _, pubErr := os.Stat(key + ".pub"); !errors.Is(pubErr, os.ErrNotExist) {
		t.Errorf("only the private key may be copied: %v", pubErr)
	}
	if entries, readErr := os.ReadDir(homeDir); readErr == nil {
		for _, entry := range entries {
			if entry.Name() != config.GitIdentityKeyFileName && entry.Name() != "git-identity.json" {
				t.Errorf("the step left %s under the Invokr home", entry.Name())
			}
		}
	}
	if data, readErr := os.ReadFile(source); readErr != nil || !slices.Equal(data, []byte("key material\n")) {
		t.Fatalf("the host key changed: %v", readErr)
	}
}

// A key that cannot be read is refused instead of storing an identity whose
// commits come out unsigned.
func TestRunGitIdentityStepRefusesAnUnreadableKey(t *testing.T) {
	homeDir, dir := t.TempDir(), t.TempDir()
	answersPath, outPath := filepath.Join(dir, "answers"), filepath.Join(dir, "out")
	missing := filepath.Join(dir, "nope", "id_ed25519_signer")
	if err := os.WriteFile(answersPath, []byte("octocat-bot\noctocat-bot@example.com\n"+missing+"\n"), 0o600); err != nil {
		t.Fatalf("answers: %v", err)
	}
	in, err := os.Open(answersPath)
	if err != nil {
		t.Fatalf("open answers: %v", err)
	}
	t.Cleanup(func() { _ = in.Close() })
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create out: %v", err)
	}
	_, stepErr := RunGitIdentityStep(in, out, homeDir)
	if stepErr == nil {
		t.Fatalf("an unreadable key must be refused")
	}
	if !strings.Contains(stepErr.Error(), missing) {
		t.Errorf("the error must name the answered key path %q: %v", missing, stepErr)
	}
	if _, err := os.Stat(config.GitIdentityPath(homeDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("nothing must be stored: %v", err)
	}
}
