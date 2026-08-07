package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-ai/invokr/constants"
)

func TestLoadFileMissingUsesDefaults(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())

	cfg, err := LoadFile(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("expected missing config file to be ignored, got %v", err)
	}

	if cfg.Provider.BaseURL != constants.DefaultProviderBaseURL {
		t.Fatalf("expected default base URL, got %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.Model != constants.DefaultProviderModel {
		t.Fatalf("expected default model, got %q", cfg.Provider.Model)
	}
	if cfg.Agent.MaxTurns != constants.DefaultAgentMaxTurns {
		t.Fatalf("expected default max turns, got %d", cfg.Agent.MaxTurns)
	}
}

func TestLoadFilePartialConfigPreservesDefaults(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":{"base_url":"http://file.local:8080"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}

	if cfg.Provider.BaseURL != "http://file.local:8080" {
		t.Fatalf("expected file base URL, got %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.Model != constants.DefaultProviderModel {
		t.Fatalf("expected default model, got %q", cfg.Provider.Model)
	}
	if cfg.Agent.NPredict != constants.DefaultAgentNPredict {
		t.Fatalf("expected default n_predict, got %d", cfg.Agent.NPredict)
	}
}

func TestLoadFileNestedProviderConfig(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":{"base_url":"http://provider.local:8080","model":"custom-model","context_window_tokens":64000}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}

	if cfg.Provider.BaseURL != "http://provider.local:8080" {
		t.Fatalf("expected nested provider base URL, got %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.Model != "custom-model" {
		t.Fatalf("expected nested provider model, got %q", cfg.Provider.Model)
	}
	if cfg.Provider.ContextWindowTokens != 64000 {
		t.Fatalf("expected nested provider context window, got %d", cfg.Provider.ContextWindowTokens)
	}
}

func TestLoadFileShellConfig(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"shell":{"enabled":false,"allowed_commands":["ls","git","go"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}

	if cfg.Shell.IsEnabled() {
		t.Fatal("expected shell enabled=false from config file")
	}
	// Configured commands come first; the read-only baseline is unioned in.
	if !slices.Equal(cfg.Shell.AllowedCommands[:3], []string{"ls", "git", "go"}) {
		t.Fatalf("expected configured shell commands, got %#v", cfg.Shell.AllowedCommands)
	}
	if !slices.Contains(cfg.Shell.AllowedCommands, "find") {
		t.Fatalf("expected read-only baseline in shell commands, got %#v", cfg.Shell.AllowedCommands)
	}
}

func TestLoadFileIgnoresRemovedMemoryRecallConfig(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"memory":{"recall":{"tier2":{"backend":"llamavector","limit":3}}}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}
	if cfg.Memory.Zerfoo.Enabled {
		t.Fatal("expected removed recall config to have no effect on memory runtime")
	}
}

func TestLoadFileMemoryZerfooConfig(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"memory":{"zerfoo":{"enabled":true,"embedding_model":"Custom/Embed","summarization_path":"/models/sum.gguf","embedding_dimensions":768,"request_timeout_ms":1500,"max_summary_tokens":256}}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}
	z := cfg.Memory.Zerfoo
	if !z.Enabled || z.EmbeddingModel != "Custom/Embed" || z.SummarizationPath != "/models/sum.gguf" {
		t.Fatalf("unexpected zerfoo config: %#v", z)
	}
	if z.EmbeddingDimensions != 768 || z.RequestTimeoutMS != 1500 || z.MaxSummaryTokens != 256 {
		t.Fatalf("unexpected zerfoo sizing: %#v", z)
	}
}

func TestLoadFileSkillsConfig(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	skillsDir := filepath.Join(t.TempDir(), "skills")
	if err := os.WriteFile(path, []byte(`{"skills":{"dir":"`+skillsDir+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}

	if cfg.Skills.Dir != skillsDir {
		t.Fatalf("expected configured skills dir, got %q", cfg.Skills.Dir)
	}
}

func TestLoadFileIgnoresRemovedApplyPatchKeys(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"apply_patch_enabled":false,"apply_patch":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadFile(path); err != nil {
		t.Fatalf("expected config with removed apply_patch keys to load, got %v", err)
	}
}

func TestLoadFileLegacyTopLevelBaseURLUsesDefault(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"base_url":"http://llm.local:8080"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}

	if cfg.Provider.BaseURL != constants.DefaultProviderBaseURL {
		t.Fatalf("expected deprecated top-level base_url to be ignored, got %q", cfg.Provider.BaseURL)
	}
}

func TestLoadFileEnvOverridesFile(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	t.Setenv("INVOKR_BASE_URL", "http://env.local:8080")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":{"base_url":"http://file.local:8080"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}

	if cfg.Provider.BaseURL != "http://env.local:8080" {
		t.Fatalf("expected env base URL to override file, got %q", cfg.Provider.BaseURL)
	}
}

// parallel_tool_calls is not a real config knob: ParallelToolCalls is always
// derived from parallel_tool_calls_disabled, since a JSON bool can't
// distinguish an explicit false from an unset field. An explicit
// "parallel_tool_calls": false must have no effect; only the _disabled
// knob does.
func TestLoadFileParallelToolCallsFieldIsIgnored(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"parallel_tool_calls":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}
	if !cfg.Agent.ParallelToolCalls {
		t.Fatal("expected parallel_tool_calls:false in JSON to have no effect")
	}
}

func TestLoadFileParallelToolCallsDisabledKnobWorks(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"parallel_tool_calls_disabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}
	if cfg.Agent.ParallelToolCalls {
		t.Fatal("expected parallel_tool_calls_disabled:true to disable parallel tool calls")
	}
}

func TestLoadFileMalformedJSONReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"base_url":`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected malformed JSON to return an error")
	}
}

// Precedence is flags > env > file > defaults. The flag half lives in
// cmd.TestRootFlagOverridesLoadedEnvironment; this covers env > file >
// defaults, and that WithDefaults stays pure so downstream re-resolution
// cannot resurrect an env value over an explicit flag.
func TestLoadFileEnvOverridesFileButDefaultsStayPure(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	t.Setenv("INVOKR_MODEL", "env-model")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":{"model":"file-model","base_url":"http://file"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.Model != "env-model" {
		t.Fatalf("model = %q, want env-model", cfg.Provider.Model)
	}
	if cfg.Provider.BaseURL != "http://file" {
		t.Fatalf("base_url = %q, want the file value", cfg.Provider.BaseURL)
	}

	cfg.Provider.Model = "flag-model"
	if got := cfg.WithDefaults().Provider.Model; got != "flag-model" {
		t.Fatalf("WithDefaults re-read the environment: model = %q, want flag-model", got)
	}
}

func TestDefaultConfigPathUsesInvokrHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_HOME", root)

	if got := DefaultConfigPath(); got != filepath.Join(root, constants.DefaultConfigFile) {
		t.Fatalf("expected default config path under INVOKR_HOME, got %q", got)
	}
}

func TestSetSetupFieldsKeepsUnrelatedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	in := `{"provider":{"base_url":"http://old","model":"old","router":{"active":"x"}},"tools":{"shell":true},"memory":{"zerfoo":{"enabled":false,"model":"z"}}}`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetSetupFields(path, "http://new", "m2", true); err != nil {
		t.Fatalf("SetSetupFields: %v", err)
	}
	var raw map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	provider := raw["provider"].(map[string]any)
	zerfoo := raw["memory"].(map[string]any)["zerfoo"].(map[string]any)
	if provider["base_url"] != "http://new" || provider["model"] != "m2" || zerfoo["enabled"] != true {
		t.Fatalf("fields not set: %s", data)
	}
	if provider["router"].(map[string]any)["active"] != "x" || zerfoo["model"] != "z" || raw["tools"].(map[string]any)["shell"] != true {
		t.Fatalf("unrelated keys changed: %s", data)
	}
}
