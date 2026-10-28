package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

func TestToolTemplatesSeedPreserveAndReset(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsureAt(home); err != nil {
		t.Fatal(err)
	}
	dir := ToolTemplatesDir(home)
	for name, want := range map[string][]string{
		"researcher": {"tools", "read", "web_search", "web_fetch"},
		"planner":    {"tools", "read", "web_search", "web_fetch", "todo"},
		"coder":      {},
	} {
		data, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil || !json.Valid(data) {
			t.Fatalf("%s template: %v", name, err)
		}
		var cfg config.AppConfig
		if err := json.Unmarshal(data, &cfg); err != nil || !slices.Equal(cfg.Tools.Allowlist, want) {
			t.Fatalf("%s tools = %v: %v", name, cfg.Tools.Allowlist, err)
		}
		profile, err := config.LoadProfile(home, name)
		if err != nil || profile.Tools == nil || profile.Tools.Allowlist == nil || !slices.Equal(*profile.Tools.Allowlist, want) {
			t.Fatalf("%s profile tools = %v: %v", name, profile.Tools, err)
		}
		if profile.Mode == nil || *profile.Mode != map[string]string{"researcher": "auto", "planner": "plan", "coder": "manual"}[name] {
			t.Fatalf("%s mode = %v", name, profile.Mode)
		}
	}
	path := filepath.Join(dir, "researcher.json")
	if err := os.WriteFile(path, []byte(`{"tools":{"allowlist":["read"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAt(home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"tools":{"allowlist":["read"]}}` {
		t.Fatalf("setup replaced edited template: %s %v", data, err)
	}
	if _, err := EnsureConfigured(home, Answers{ProviderBaseURL: DefaultProviderBaseURL, ResetToolTemplates: true}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) == `{"tools":{"allowlist":["read"]}}` {
		t.Fatalf("reset did not restore template: %s %v", data, err)
	}
}
