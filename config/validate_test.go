package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestValidateRejectsNegativeValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AppConfig)
	}{
		{"http_timeout", func(c *AppConfig) { c.Provider.HTTPTimeout = -time.Second }},
		{"stall_timeout", func(c *AppConfig) { c.Provider.StallTimeout = -time.Second }},
		{"max_retries", func(c *AppConfig) { c.Provider.MaxRetries = -1 }},
		{"max_turns", func(c *AppConfig) { c.Agent.MaxTurns = -1 }},
		{"zerfoo_request_timeout", func(c *AppConfig) { c.Memory.Zerfoo.RequestTimeoutMS = -1 }},
		{"toolbox_exec_timeout", func(c *AppConfig) { c.Toolbox.ExecTimeoutMS = -1 }},
		{"code_interpreter_default_timeout", func(c *AppConfig) { c.CodeInterpreter.DefaultTimeoutMS = -1 }},
		{"code_interpreter_max_timeout", func(c *AppConfig) { c.CodeInterpreter.MaxTimeoutMS = -1 }},
	}
	if err := (AppConfig{}).Validate(nil); err != nil {
		t.Fatalf("Validate(zero config) error = %v, want nil", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AppConfig{}
			tc.mutate(&cfg)
			if err := cfg.Validate(nil); err == nil {
				t.Fatal("Validate() error = nil, want negative-value error")
			}
		})
	}
}

func TestValidateRejectsInvalidContextThresholds(t *testing.T) {
	for _, agent := range []AgentConfig{
		{ContextWarningPercent: 70, ContextCompactPercent: -1},
		{ContextWarningPercent: 70, ContextCompactPercent: 70},
		{ContextWarningPercent: 80, ContextCompactPercent: 70},
		{ContextWarningPercent: 0, ContextCompactPercent: 85},
		{ContextWarningPercent: 70, ContextCompactPercent: 100},
		{ContextWarningPercent: 80},
		{ContextWarningPercent: -1},
	} {
		if err := (AppConfig{Agent: agent}).Validate(nil); err == nil {
			t.Fatalf("Validate(%#v) error = nil", agent)
		}
	}
	for _, agent := range []AgentConfig{
		{},
		{ContextWarningPercent: 70},
		{ContextWarningPercent: 1, ContextCompactPercent: 2},
		{ContextWarningPercent: 98, ContextCompactPercent: 99},
	} {
		if err := (AppConfig{Agent: agent}).Validate(nil); err != nil {
			t.Fatalf("Validate(%#v) rejected valid thresholds: %v", agent, err)
		}
	}
}

func TestValidateWarnsOnMissingDirs(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	logger := zap.New(core)
	cfg := AppConfig{}
	cfg.Skills.Dir = "/nonexistent/skills"
	cfg.Agents.Dir = t.TempDir() // exists: must not warn
	cfg.Toolbox.Dir = "/nonexistent/toolbox"
	if err := cfg.Validate(logger); err != nil {
		t.Fatalf("Validate() error = %v, want nil (missing dirs warn, not fail)", err)
	}
	warned := map[string]bool{}
	for _, entry := range logs.All() {
		for _, field := range entry.Context {
			if field.Key == "key" {
				warned[field.String] = true
			}
		}
	}
	if !warned["skills.dir"] || !warned["toolbox.dir"] {
		t.Fatalf("warned keys = %v, want skills.dir and toolbox.dir", warned)
	}
	if warned["agents.dir"] {
		t.Fatalf("warned about existing agents.dir: %v", warned)
	}
}

func TestSecretRedactionInStringFormatting(t *testing.T) {
	cfg := AppConfig{}
	cfg.Provider.APIKey = "sk-super-secret-value-1234"
	cfg.Messaging.Telegram.Token = "123456:telegram-secret-token"
	dumps := []string{
		fmt.Sprintf("%+v", cfg.Provider),
		fmt.Sprintf("%v", cfg.Provider),
		fmt.Sprintf("%+v", cfg.Messaging.Telegram),
	}
	for _, dump := range dumps {
		if strings.Contains(dump, "sk-super-secret-value-1234") || strings.Contains(dump, "123456:telegram-secret-token") {
			t.Fatalf("secret leaked in formatted config: %s", dump)
		}
	}
	if got := maskSecret("sk-super-secret-value-1234"); got != "****1234" {
		t.Fatalf("maskSecret() = %q, want ****1234", got)
	}
	if got := maskSecret("abc"); got != "****" {
		t.Fatalf("maskSecret(short) = %q, want ****", got)
	}
	if got := maskSecret(""); got != "" {
		t.Fatalf("maskSecret(empty) = %q, want empty", got)
	}
}

func TestValidateMCPTransportExclusivity(t *testing.T) {
	cases := []struct {
		name    string
		server  MCPServerConfig
		wantErr bool
	}{
		{"stdio", MCPServerConfig{Command: "srv", Enabled: true}, false},
		{"http", MCPServerConfig{URL: "https://mcp.example.com", Enabled: true}, false},
		{"both", MCPServerConfig{Command: "srv", URL: "https://mcp.example.com", Enabled: true}, true},
		{"neither", MCPServerConfig{Enabled: true}, true},
		{"disabled and empty", MCPServerConfig{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AppConfig{MCP: MCPConfig{Servers: map[string]MCPServerConfig{"srv": tc.server}}}.WithDefaults()
			err := cfg.Validate(zap.NewNop())
			if tc.wantErr && (err == nil || !strings.Contains(err.Error(), "exactly one of command or url")) {
				t.Fatalf("expected transport exclusivity error, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateRejectsUnknownToolResultStrategy(t *testing.T) {
	cfg := AppConfig{}
	cfg.Agent.ToolResultStrategy = "shrink-ray"
	if err := cfg.Validate(nil); err == nil {
		t.Fatalf("unknown default strategy accepted")
	}
	cfg = AppConfig{}
	cfg.Agent.ToolResultStrategies = map[string]string{"shell": "nope"}
	if err := cfg.Validate(nil); err == nil {
		t.Fatalf("unknown per-tool strategy accepted")
	}
	cfg = AppConfig{}
	cfg.Agent.ToolResultStrategy = "spill"
	cfg.Agent.ToolResultStrategies = map[string]string{"shell": "spill"}
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("valid strategies rejected: %v", err)
	}
}

func TestValidateRejectsTurnBudgetBounds(t *testing.T) {
	cfg := AppConfig{}
	cfg.Agent.ReadTurnWeight = 1.5
	if err := cfg.Validate(nil); err == nil {
		t.Fatal("read_turn_weight above 1 accepted")
	}
	cfg = AppConfig{}
	cfg.Agent.HardTurnMultiplier = 0.5
	if err := cfg.Validate(nil); err == nil {
		t.Fatal("hard_turn_multiplier below 1 accepted")
	}
	cfg = AppConfig{}
	cfg.Agent.ReadTurnWeight = 1
	cfg.Agent.HardTurnMultiplier = 2
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("valid turn budget rejected: %v", err)
	}
}

func TestValidateSkipsMissingDefaultDirs(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	core, logs := observer.New(zap.WarnLevel)
	if err := (AppConfig{}).WithDefaults().Validate(zap.New(core)); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if n := logs.FilterMessage("configured directory does not exist").Len(); n != 0 {
		t.Fatalf("got %d missing-dir warnings for default dirs, want 0", n)
	}
}

// TestValidateQuietOnFreshHome: the default dirs under a fresh INVOKR_HOME
// are not created yet, and that is not worth a warning (A3).
func TestValidateQuietOnFreshHome(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	home := t.TempDir()
	cfg := AppConfig{}
	cfg.Agents.Dir = filepath.Join(home, "agents")
	if err := cfg.Validate(zap.New(core)); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("warned on a fresh home: %v", logs.All())
	}
}
