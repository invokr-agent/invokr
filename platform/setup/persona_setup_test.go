package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/constants"
)

func TestEnsureAtCreatesPersonaMarkdown(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".invokr")

	result, err := EnsureAt(home)
	if err != nil {
		t.Fatalf("EnsureAt returned error: %v", err)
	}
	if !result.PersonaCreated {
		t.Fatal("expected PERSONA.md to be created")
	}

	data, err := os.ReadFile(result.PersonaPath)
	if err != nil {
		t.Fatalf("expected PERSONA.md to exist: %v", err)
	}
	if string(data) != defaultPersonaTemplate {
		t.Fatalf("expected default prompt to be seeded, got %q", string(data))
	}

	info, err := os.Stat(result.PersonaPath)
	if err != nil {
		t.Fatalf("stat PERSONA.md: %v", err)
	}
	if got := info.Mode().Perm(); got != memoryFilePerm {
		t.Fatalf("PERSONA.md permissions = %v, want %v", got, memoryFilePerm)
	}
}

func TestEnsureAtDoesNotOverwriteExistingPersona(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, constants.PersonaFilename)
	original := []byte("You are a custom agent.\n")
	if err := os.WriteFile(path, original, memoryFilePerm); err != nil {
		t.Fatal(err)
	}

	result, err := EnsureAt(home)
	if err != nil {
		t.Fatalf("EnsureAt returned error: %v", err)
	}
	if result.PersonaCreated {
		t.Fatal("did not expect existing PERSONA.md to be recreated")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("expected existing PERSONA.md to be preserved, got %q", string(data))
	}
}

// TestPersonaTemplateMirrorsDefaultPrompt pins the shipped template to
// constants.DefaultAgentSystemPrompt. They must not drift: a home with no
// PERSONA.md falls back to the constant, so any difference would make a
// seeded install and a fresh one render different system prompts.
func TestPersonaTemplateMirrorsDefaultPrompt(t *testing.T) {
	if defaultPersonaTemplate != constants.DefaultAgentSystemPrompt {
		t.Fatalf("PERSONA.md template drifted from constants.DefaultAgentSystemPrompt:\ntemplate = %q\nconstant = %q",
			defaultPersonaTemplate, constants.DefaultAgentSystemPrompt)
	}
}

// TestEnsureAtSeedsDefaultPromptVerbatim checks the seeding path end to end:
// the bytes on disk are the constant, not merely a template that matches it.
func TestEnsureAtSeedsDefaultPromptVerbatim(t *testing.T) {
	result, err := EnsureAt(filepath.Join(t.TempDir(), ".invokr"))
	if err != nil {
		t.Fatalf("EnsureAt returned error: %v", err)
	}
	data, err := os.ReadFile(result.PersonaPath)
	if err != nil {
		t.Fatalf("read PERSONA.md: %v", err)
	}
	if string(data) != constants.DefaultAgentSystemPrompt {
		t.Fatalf("seeded PERSONA.md = %q, want %q", string(data), constants.DefaultAgentSystemPrompt)
	}
}
