package setup

import (
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

// The answered key is copied into invokr's own key dir, not referenced where it
// sits: a sandboxed commit can only sign with a file the sandbox mounts, and the
// host's own ~/.ssh is never written.
func TestRunGitIdentityStepCopiesTheSigningKey(t *testing.T) {
	homeDir, dir := t.TempDir(), t.TempDir()
	host := filepath.Join(dir, "ssh")
	if err := os.MkdirAll(host, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	source := filepath.Join(host, "id_ed25519_signer")
	for _, path := range []string{source, source + ".pub"} {
		if err := os.WriteFile(path, []byte("key material\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
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
	want := filepath.Join(config.GitIdentityKeyDir(homeDir), "id_ed25519_signer")
	if err != nil || stored.SigningKey != want {
		t.Fatalf("stored signing key = %q (%v), want the copy at %s", stored.SigningKey, err, want)
	}
	for _, path := range []string{want, want + ".pub"} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("copy %s: %v (%o)", path, statErr, info.Mode().Perm())
		}
		if data, readErr := os.ReadFile(path); readErr != nil || !slices.Equal(data, []byte("key material\n")) {
			t.Fatalf("copy %s content: %v", path, readErr)
		}
	}
	if info, statErr := os.Stat(config.GitIdentityKeyDir(homeDir)); statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("key dir mode = %o (%v)", info.Mode().Perm(), statErr)
	}
	if data, readErr := os.ReadFile(source); readErr != nil || !slices.Equal(data, []byte("key material\n")) {
		t.Fatalf("the host key changed: %v", readErr)
	}
}
