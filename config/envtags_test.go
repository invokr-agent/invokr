package config

import (
	"reflect"
	"testing"
	"time"
)

// collectEnvTags walks a struct type the way applyEnv walks a value and
// returns every environment variable name a tag claims, in field order.
func collectEnvTags(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	var names []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			names = append(names, collectEnvTags(t, field.Type)...)
			continue
		}
		if tag, ok := field.Tag.Lookup("env"); ok {
			names = append(names, splitList(tag)...)
		}
	}
	return names
}

func TestEnvTagsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range collectEnvTags(t, reflect.TypeOf(AppConfig{})) {
		if seen[name] {
			t.Fatalf("duplicate env tag %q", name)
		}
		seen[name] = true
	}
	if len(seen) < 80 {
		t.Fatalf("expected the config tree to declare ~90 env vars, got %d", len(seen))
	}
}

// TestApplyEnvTypes exercises one field of every type the walker supports.
func TestApplyEnvTypes(t *testing.T) {
	t.Setenv("INVOKR_MODEL", "m1")                              // string
	t.Setenv("INVOKR_CONTEXT_WINDOW_TOKENS", "4096")            // int
	t.Setenv("INVOKR_AGENT_MAX_SESSION_TOKENS", "9000000000")   // int64
	t.Setenv("INVOKR_TEMPERATURE", "0.25")                      // float64
	t.Setenv("INVOKR_WORKSPACE_ALLOW_HIDDEN_PATHS", "true")     // bool
	t.Setenv("INVOKR_PROVIDER_HTTP_TIMEOUT", "45s")             // time.Duration
	t.Setenv("INVOKR_SHELL_ALLOWED_COMMANDS", "ls, pwd ,, git") // []string
	t.Setenv("INVOKR_TOOLBOX_ENV", "A=1,B=2")                   // map[string]string

	cfg := AppConfig{Toolbox: ToolboxConfig{Env: map[string]string{"A": "keep", "C": "3"}}}
	applyEnv(&cfg)

	if cfg.Provider.Model != "m1" || cfg.Provider.ContextWindowTokens != 4096 {
		t.Fatalf("string/int not applied: %+v", cfg.Provider)
	}
	if cfg.Agent.MaxSessionTokens != 9000000000 || cfg.Agent.Temperature != 0.25 {
		t.Fatalf("int64/float not applied: %+v", cfg.Agent)
	}
	if !cfg.Workspace.AllowHiddenPaths {
		t.Fatal("bool not applied")
	}
	if cfg.Provider.HTTPTimeout != 45*time.Second {
		t.Fatalf("HTTPTimeout = %v, want 45s", cfg.Provider.HTTPTimeout)
	}
	if got := cfg.Shell.AllowedCommands; len(got) != 3 || got[0] != "ls" || got[1] != "pwd" || got[2] != "git" {
		t.Fatalf("AllowedCommands = %q, want [ls pwd git]", got)
	}
	// Env vars overlay the configured map rather than replacing it.
	want := map[string]string{"A": "1", "B": "2", "C": "3"}
	if got := cfg.Toolbox.Env; !reflect.DeepEqual(got, want) {
		t.Fatalf("Toolbox.Env = %v, want %v", got, want)
	}
}

// TestApplyEnvIgnoresUnsetAndUnparsable pins the contract the hand-written
// envX helpers had: a blank or malformed variable leaves the field alone.
func TestApplyEnvIgnoresUnsetAndUnparsable(t *testing.T) {
	t.Setenv("INVOKR_CONTEXT_WINDOW_TOKENS", "not-a-number")
	t.Setenv("INVOKR_MODEL", "   ")

	cfg := AppConfig{Provider: ProviderConfig{Model: "keep", ContextWindowTokens: 7}}
	applyEnv(&cfg)

	if cfg.Provider.Model != "keep" || cfg.Provider.ContextWindowTokens != 7 {
		t.Fatalf("unparsable env clobbered config: %+v", cfg.Provider)
	}
}

// TestApplyEnvLastNameWins covers a tag naming several variables.
func TestApplyEnvLastNameWins(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "from-openai")
	cfg := AppConfig{}
	applyEnv(&cfg)
	if cfg.Provider.APIKey != "from-openai" {
		t.Fatalf("APIKey = %q, want from-openai", cfg.Provider.APIKey)
	}

	t.Setenv("INVOKR_PROVIDER_API_KEY", "from-invokr")
	cfg = AppConfig{}
	applyEnv(&cfg)
	if cfg.Provider.APIKey != "from-invokr" {
		t.Fatalf("APIKey = %q, want from-invokr", cfg.Provider.APIKey)
	}
}

func TestToolQuotasFromEnv(t *testing.T) {
	t.Setenv("INVOKR_TOOL_QUOTAS", "web_search=2,web_fetch=9")
	cfg := AppConfig{}.withEnv().WithDefaults()
	if cfg.Agent.ToolQuotas["web_search"] != 2 || cfg.Agent.ToolQuotas["web_fetch"] != 9 {
		t.Fatalf("quotas = %v", cfg.Agent.ToolQuotas)
	}
	// Tools the override did not name keep their default quota.
	if cfg.Agent.ToolQuotas["delegate"] != 3 {
		t.Fatalf("delegate quota = %d, want the default 3", cfg.Agent.ToolQuotas["delegate"])
	}
}
