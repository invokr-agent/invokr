package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-agent/invokr/constants"
)

func writePersona(t *testing.T, home, content string) {
	t.Helper()
	if err := os.WriteFile(PersonaPath(home), []byte(content), 0o600); err != nil {
		t.Fatalf("write PERSONA.md: %v", err)
	}
}

func TestLoadPersonaFile(t *testing.T) {
	home := t.TempDir()

	if _, ok := loadPersonaFile(home); ok {
		t.Fatalf("missing file should not load")
	}

	writePersona(t, home, "  \n\t\n")
	if _, ok := loadPersonaFile(home); ok {
		t.Fatalf("whitespace-only file should not load")
	}

	writePersona(t, home, "\nYou are a careful reviewer.\n")
	prompt, ok := loadPersonaFile(home)
	if !ok || prompt != "You are a careful reviewer." {
		t.Fatalf("got %q, ok=%v", prompt, ok)
	}
}

func TestLoadPersonaFileSkipsIrregular(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(PersonaPath(home), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, ok := loadPersonaFile(home); ok {
		t.Fatalf("directory should not load")
	}

	home2 := t.TempDir()
	target := filepath.Join(home2, "real.md")
	if err := os.WriteFile(target, []byte("prompt"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(target, PersonaPath(home2)); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, ok := loadPersonaFile(home2); ok {
		t.Fatalf("symlink should not load")
	}
}

func TestPersonaPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	t.Setenv("INVOKR_SYSTEM_PROMPT", "")

	resolve := func(cfg AppConfig) AppConfig {
		t.Helper()
		resolved, err := resolveLoadedConfig(cfg, "")
		if err != nil {
			t.Fatalf("resolveLoadedConfig: %v", err)
		}
		return resolved
	}

	// No PERSONA.md: the config value (or default) stands.
	cfg := resolve(AppConfig{})
	if cfg.Agent.SystemPrompt != constants.DefaultAgentSystemPrompt {
		t.Fatalf("expected default prompt, got %q", cfg.Agent.SystemPrompt)
	}

	// PERSONA.md beats the config-file value.
	writePersona(t, home, "From PERSONA.md")
	fileCfg := AppConfig{}
	fileCfg.Agent.SystemPrompt = "from config.json"
	cfg = resolve(fileCfg)
	if cfg.Agent.SystemPrompt != "From PERSONA.md" {
		t.Fatalf("expected PERSONA.md to win over config, got %q", cfg.Agent.SystemPrompt)
	}

	// Env beats PERSONA.md.
	t.Setenv("INVOKR_SYSTEM_PROMPT", "from env")
	cfg = resolve(AppConfig{})
	if cfg.Agent.SystemPrompt != "from env" {
		t.Fatalf("expected env to win over PERSONA.md, got %q", cfg.Agent.SystemPrompt)
	}
}
