package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProfile(t *testing.T, home, name, content string) {
	t.Helper()
	dir := ProfilesDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

func routerConfig() RouterConfig {
	return RouterConfig{
		Active: "local",
		Providers: map[string]RouterProviderConfig{
			"local":     {Type: "openai-compatible", BaseURL: "http://localhost:8080"},
			"anthropic": {Type: "anthropic"},
		},
		Profiles: map[string]RouterProfileConfig{
			"local": {Provider: "local", Model: "llama"},
		},
	}
}

func TestLoadProfileParsesFrontMatterAndBody(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, "fast", "---\nmodel: mini\ntemperature: 0.2\n---\nBe quick.\n")

	p, err := LoadProfile(home, "fast")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Model == nil || *p.Model != "mini" {
		t.Fatalf("model = %v", p.Model)
	}
	if p.Temperature == nil || *p.Temperature != 0.2 {
		t.Fatalf("temperature = %v", p.Temperature)
	}
	if p.Provider != nil {
		t.Fatalf("provider should be absent, got %v", *p.Provider)
	}
	if p.SystemPrompt != "Be quick." {
		t.Fatalf("prompt = %q", p.SystemPrompt)
	}
}

func TestLoadProfileProseOnly(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, "plain", "Just a prompt.\n")

	p, err := LoadProfile(home, "plain")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Model != nil || p.Provider != nil || p.Temperature != nil {
		t.Fatalf("expected no overrides, got %+v", p)
	}
	if p.SystemPrompt != "Just a prompt." {
		t.Fatalf("prompt = %q", p.SystemPrompt)
	}
}

func TestLoadProfileBadYAML(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, "bad", "---\nmodel: [unclosed\n---\nbody\n")
	if _, err := LoadProfile(home, "bad"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestListProfiles(t *testing.T) {
	home := t.TempDir()
	if names, err := ListProfiles(home); err != nil || names != nil {
		t.Fatalf("absent dir: %v %v", names, err)
	}
	writeProfile(t, home, "b", "x")
	writeProfile(t, home, "a", "x")
	names, err := ListProfiles(home)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("names = %v", names)
	}
}

func TestApplyNamedProfileFileOverridesRouter(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, "claude", "---\nprovider: anthropic\nmodel: claude-x\n---\nYou are Claude-ish.\n")

	cfg := AppConfig{}
	cfg.Provider.Router = routerConfig()
	out, err := ApplyNamedProfile(cfg, home, "claude")
	if err != nil {
		t.Fatalf("ApplyNamedProfile: %v", err)
	}
	if out.Provider.Router.Active != "claude" {
		t.Fatalf("active = %q", out.Provider.Router.Active)
	}
	entry := out.Provider.Router.Profiles["claude"]
	if entry.Provider != "anthropic" || entry.Model != "claude-x" {
		t.Fatalf("entry = %+v", entry)
	}
	if out.Provider.Model != "claude-x" {
		t.Fatalf("model = %q", out.Provider.Model)
	}
	if out.Agent.SystemPrompt != "You are Claude-ish." {
		t.Fatalf("prompt = %q", out.Agent.SystemPrompt)
	}
}

func TestApplyNamedProfileFallsBackToRouterTable(t *testing.T) {
	home := t.TempDir()
	cfg := AppConfig{}
	cfg.Provider.Router = routerConfig()
	cfg.Provider.Router.Profiles["extra"] = RouterProfileConfig{Provider: "local", Model: "other"}

	out, err := ApplyNamedProfile(cfg, home, "extra")
	if err != nil {
		t.Fatalf("ApplyNamedProfile: %v", err)
	}
	if out.Provider.Router.Active != "extra" {
		t.Fatalf("active = %q", out.Provider.Router.Active)
	}
}

func TestApplyNamedProfileErrors(t *testing.T) {
	home := t.TempDir()
	cfg := AppConfig{}
	if _, err := ApplyNamedProfile(cfg, home, "nope"); err == nil {
		t.Fatal("expected unknown-profile error")
	}

	writeProfile(t, home, "orphan", "---\nprovider: anthropic\n---\nx\n")
	if _, err := ApplyNamedProfile(cfg, home, "orphan"); err == nil {
		t.Fatal("expected no-router error")
	}

	cfg.Provider.Router = routerConfig()
	writeProfile(t, home, "ghost", "---\nprovider: missing\n---\nx\n")
	if _, err := ApplyNamedProfile(cfg, home, "ghost"); err == nil {
		t.Fatal("expected unknown-provider error")
	}
}

func TestApplyNamedProfileModelOnlyWithoutRouter(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, "mini", "---\nmodel: tiny\n---\n")

	cfg := AppConfig{}
	out, err := ApplyNamedProfile(cfg, home, "mini")
	if err != nil {
		t.Fatalf("ApplyNamedProfile: %v", err)
	}
	if out.Provider.Model != "tiny" {
		t.Fatalf("model = %q", out.Provider.Model)
	}
	if out.Agent.SystemPrompt != "" {
		t.Fatalf("prompt should be untouched, got %q", out.Agent.SystemPrompt)
	}
}
