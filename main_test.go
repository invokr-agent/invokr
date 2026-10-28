package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/setup"
)

func TestLoadStartupConfigRunsSetupBeforeConfigLoad(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".invokr")
	t.Setenv("INVOKR_HOME", home)

	cfg, err := loadStartupConfig()
	if err != nil {
		t.Fatalf("loadStartupConfig returned error: %v", err)
	}

	if cfg.Provider.BaseURL != constants.DefaultProviderBaseURL {
		t.Fatalf("expected default base URL, got %q", cfg.Provider.BaseURL)
	}
	configPath := filepath.Join(home, constants.DefaultConfigFile)
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected setup to create default config: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("expected default config to be readable: %v", err)
	}
	var stored config.AppConfig
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("expected default config JSON: %v", err)
	}
	if stored.Provider.BaseURL != constants.DefaultProviderBaseURL {
		t.Fatalf("expected non-interactive startup config to keep default base URL, got %q", stored.Provider.BaseURL)
	}
	if stored.Memory.Dir != home {
		t.Fatalf("expected startup config memory dir under INVOKR_HOME, got %q", stored.Memory.Dir)
	}
}

func TestSetupCanResetMalformedActiveJSONProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	if _, err := setup.EnsureAt(home); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(`{"profile":"researcher"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(config.ProfilesDir(home), "researcher.json")
	if err := os.WriteFile(profilePath, []byte(`{"mode":"bogus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Args
	os.Args = []string{"invokr", "setup"}
	t.Cleanup(func() { os.Args = previous })
	if _, err := loadStartupConfig(); err != nil {
		t.Fatalf("setup preflight rejected malformed profile: %v", err)
	}
	if err := setup.WriteToolTemplates(home, true); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"invokr", "agent"}
	if _, err := loadStartupConfig(); err != nil {
		t.Fatalf("reset profile did not recover startup: %v", err)
	}
}

func TestConfigUseCanReplaceMalformedActiveProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	if _, err := setup.EnsureAt(home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"profile":"researcher"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.ProfilesDir(home), "researcher.json"), []byte(`{"mode":"bogus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Args
	os.Args = []string{"invokr", "config", "use", "coder"}
	t.Cleanup(func() { os.Args = previous })
	if _, err := loadStartupConfig(); err != nil {
		t.Fatalf("config use preflight rejected malformed profile: %v", err)
	}
}

func TestStartupChoiceTreatsPromptAsData(t *testing.T) {
	for _, tc := range []struct {
		args             []string
		profile          string
		setup, configUse bool
	}{
		{args: []string{"invokr"}},
		{args: []string{"invokr", "setup"}, setup: true},
		{args: []string{"invokr", "setup", "--help"}, setup: true},
		{args: []string{"invokr", "--help"}},
		{args: []string{"invokr", "config", "--help"}},
		{args: []string{"invokr", "--version"}},
		{args: []string{"invokr", "config", "use", "coder"}, configUse: true},
		{args: []string{"invokr", "exec", "setup"}},
		{args: []string{"invokr", "--profile", "researcher", "exec", "setup"}, profile: "researcher"},
		{args: []string{"invokr", "exec", "--", "--profile=coder"}},
	} {
		got, err := parseStartupChoice(tc.args)
		if err != nil || got.profile != tc.profile || got.setup != tc.setup || got.configUse != tc.configUse {
			t.Fatalf("parseStartupChoice(%v) = %+v, %v", tc.args, got, err)
		}
	}
}

func TestPromptTextCannotBroadenStoredProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	if _, err := setup.EnsureAt(home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"profile":"researcher"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	for _, args := range [][]string{{"invokr", "exec", "setup"}, {"invokr", "exec", "--", "--profile=coder"}} {
		os.Args = args
		cfg, err := loadStartupConfig()
		if err != nil || cfg.SessionMode != "auto" || len(cfg.Tools.Allowlist) == 0 {
			t.Fatalf("args=%v lost stored researcher profile: mode=%q tools=%v err=%v", args, cfg.SessionMode, cfg.Tools.Allowlist, err)
		}
	}
}
