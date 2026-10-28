package agentui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

type fakePromptSession struct {
	fakeSession
	prompt string
}

func (s *fakePromptSession) SetSystemPrompt(prompt string) { s.prompt = prompt }

func writeTestProfile(t *testing.T, home, name, content string) {
	t.Helper()
	dir := config.ProfilesDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProfileCommandListsAndSwitches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	session := &fakePromptSession{}

	if got := profileCommandText(session, nil); !strings.Contains(got, "no profiles in") {
		t.Fatalf("empty list = %q", got)
	}

	writeTestProfile(t, home, "fast", "---\nmodel: mini\n---\nBe quick.\n")
	if got := profileCommandText(session, nil); !strings.Contains(got, "fast") {
		t.Fatalf("list = %q", got)
	}

	out := profileCommandText(session, []string{"fast"})
	if session.model != "mini" {
		t.Fatalf("model = %q, want mini", session.model)
	}
	if session.prompt != "Be quick." {
		t.Fatalf("prompt = %q", session.prompt)
	}
	if !strings.Contains(out, "model mini") || !strings.Contains(out, "system prompt") {
		t.Fatalf("out = %q", out)
	}
}

func TestProfileCommandDefersProviderAndTemperature(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	writeTestProfile(t, home, "claude", "---\nprovider: anthropic\ntemperature: 0.3\n---\n")

	out := profileCommandText(&fakePromptSession{}, []string{"claude"})
	if !strings.Contains(out, "restart") || !strings.Contains(out, "provider") || !strings.Contains(out, "temperature") {
		t.Fatalf("out = %q", out)
	}
}

func TestProfileCommandUnknownName(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	if out := profileCommandText(&fakePromptSession{}, []string{"nope"}); !strings.Contains(out, "error:") {
		t.Fatalf("out = %q", out)
	}
}
