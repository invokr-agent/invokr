package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
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

func TestSetupRequested(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"invokr"}, false},
		{[]string{"invokr", "setup"}, true},
		{[]string{"invokr", "init"}, true},
		{[]string{"invokr", "agent"}, false},
	} {
		if got := setupRequested(tc.args); got != tc.want {
			t.Fatalf("setupRequested(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
