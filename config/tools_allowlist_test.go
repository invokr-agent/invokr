package config

import (
	"slices"
	"testing"
)

func TestToolsAllowlistFromEnv(t *testing.T) {
	t.Setenv("INVOKR_TOOLS", "read_file, shell ,, apply_patch")

	cfg := AppConfig{}
	applyEnv(&cfg)

	want := []string{"read_file", "shell", "apply_patch"}
	if !slices.Equal(cfg.Tools.Allowlist, want) {
		t.Fatalf("allowlist = %v, want %v", cfg.Tools.Allowlist, want)
	}
}

func TestToolsAllowlistDefaultsEmpty(t *testing.T) {
	if got := (AppConfig{}).WithDefaults().Tools.Allowlist; len(got) != 0 {
		t.Fatalf("default allowlist = %v, want empty (advertise everything)", got)
	}
}

func TestTodoDisabledFromEnv(t *testing.T) {
	t.Setenv("INVOKR_TODO_DISABLED", "true")
	cfg := AppConfig{}
	applyEnv(&cfg)
	if !cfg.Tools.TodoDisabled {
		t.Fatal("todo disable flag not applied")
	}
}

func TestToolsAllowlistMinimalPresetExpands(t *testing.T) {
	t.Setenv("INVOKR_TOOLS", "minimal")
	cfg := AppConfig{}
	applyEnv(&cfg)
	got := cfg.WithDefaults().Tools.Allowlist
	if !slices.Contains(got, "shell") || !slices.Contains(got, "edit") || slices.Contains(got, "minimal") || len(got) < 4 {
		t.Fatalf("minimal preset expanded to %v", got)
	}
}
