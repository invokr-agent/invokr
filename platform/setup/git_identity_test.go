package setup

import (
	"os"
	"path/filepath"
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
	defer in.Close()
	if err != nil {
		t.Fatalf("open answers: %v", err)
	}
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
	defer in.Close()
	out, err := os.Create(outPath)
	if _, err := RunGitIdentityStep(in, out, homeDir); err == nil {
		t.Errorf("a missing email must be refused, not stored")
	}
	stored, err := config.LoadGitIdentity(homeDir)
	if err != nil || stored.Name != "" {
		t.Errorf("nothing must be stored: %v (%v)", stored, err)
	}
}
