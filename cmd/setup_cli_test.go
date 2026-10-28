package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/logging"
	"github.com/invokr-agent/invokr/platform/setup"

	"github.com/stretchr/testify/require"
)

func TestSetupCmdUsesQuestionnaireDefaults(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)

	cmd := NewSetupCmd()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader("\n\n\n\n\n"))
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup command returned error: %v", err)
	}

	home := filepath.Join(userHome, ".invokr")
	cfgPath := filepath.Join(home, constants.DefaultConfigFile)
	cfg := readSetupConfig(t, cfgPath)
	if cfg.Provider.BaseURL != constants.DefaultProviderBaseURL {
		t.Fatalf("expected default base URL, got %q", cfg.Provider.BaseURL)
	}
	if !strings.Contains(out.String(), "Invokr setup: LLM provider base URL") {
		t.Fatalf("expected base URL prompt, got %q", out.String())
	}
	if !strings.Contains(out.String(), "Invokr home path") {
		t.Fatalf("expected Invokr home prompt, got %q", out.String())
	}
	if !strings.Contains(out.String(), "Config: "+cfgPath) {
		t.Fatalf("expected config path in output, got %q", out.String())
	}
	if !strings.Contains(out.String(), "JSON profiles: "+setup.ToolTemplatesDir(home)) {
		t.Fatalf("expected tool templates path in output, got %q", out.String())
	}
}

func TestSetupCmdPrefillsExistingConfigAnswers(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home := filepath.Join(userHome, ".invokr")
	cfgPath := filepath.Join(home, constants.DefaultConfigFile)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(
		`{"provider":{"base_url":"http://configured:8080","model":"configured-model"},`+
			`"memory":{"zerfoo":{"enabled":true}},`+
			`"workspace":{"reads":"ask"},`+
			`"ui":{"syntax_theme":"github-light"}}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewSetupCmd()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader("\n\n\n\n\n"))
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup command returned error: %v", err)
	}

	cfg := readSetupConfig(t, cfgPath)
	if cfg.Provider.BaseURL != "http://configured:8080" {
		t.Fatalf("expected configured base URL, got %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.Model != "configured-model" {
		t.Fatalf("expected configured model, got %q", cfg.Provider.Model)
	}
	if !cfg.Memory.Zerfoo.Enabled {
		t.Fatal("expected configured memory enabled")
	}
	if cfg.Workspace.Reads != "ask" {
		t.Fatalf("expected configured reads, got %q", cfg.Workspace.Reads)
	}
	if cfg.UI.SyntaxTheme != "github-light" {
		t.Fatalf("expected configured syntax theme, got %q", cfg.UI.SyntaxTheme)
	}
}

func TestSetupCmdUsesCustomAnswers(t *testing.T) {
	home := filepath.Join(t.TempDir(), "invokr-home")

	cmd := NewSetupCmd()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader("http://provider.local:9090\n" + home + "\nno\ncatppuccin-mocha\nno\n"))
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup command returned error: %v", err)
	}

	cfgPath := filepath.Join(home, constants.DefaultConfigFile)
	cfg := readSetupConfig(t, cfgPath)
	if cfg.Provider.BaseURL != "http://provider.local:9090" {
		t.Fatalf("expected custom base URL, got %q", cfg.Provider.BaseURL)
	}
	if !strings.Contains(out.String(), "Invokr home: "+home) {
		t.Fatalf("expected custom home in output, got %q", out.String())
	}
}

func TestRootSetupCmdSkipsRuntimeBootstrap(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	root, cleanup := NewRootCmd(config.AppConfig{})
	defer func() { _ = cleanup() }()
	var out bytes.Buffer
	root.SetIn(strings.NewReader("\n\n\n\n\n"))
	root.SetOut(&out)
	root.SetArgs([]string{"setup"})

	if err := root.Execute(); err != nil {
		t.Fatalf("root setup command returned error: %v", err)
	}
}

func TestRootCmdIncludesLogViewFlag(t *testing.T) {
	root, _ := NewRootCmd(config.AppConfig{})

	flag := root.Flags().Lookup("log-view")
	require.NotNil(t, flag, "expected log-view flag")
	if flag.DefValue != "false" {
		t.Fatalf("log-view default = %q, want false", flag.DefValue)
	}
	skills, _, _ := root.Find([]string{"skills"})
	if skills.Flags().Lookup("yolo") != nil || skills.InheritedFlags().Lookup("yolo") != nil {
		t.Fatal("session flags leaked onto skills")
	}
}

func TestConfigureAgentCommandLogging(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	tests := []struct {
		name             string
		logView          bool
		logOutputChanged bool
		wantOutput       string
		wantBuffer       bool
		existingBuffer   bool
	}{
		{name: "default file without log view", wantOutput: config.DefaultLogPath()},
		{name: "log view adds panel buffer", logView: true, wantOutput: config.DefaultLogPath(), wantBuffer: true},
		{name: "explicit log output is honored", logOutputChanged: true, wantOutput: config.DefaultOutput},
		{name: "disabled log view clears stale buffer", existingBuffer: true, wantOutput: config.DefaultLogPath()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appConfig := config.AppConfig{Log: config.LoggingConfig{Output: config.DefaultOutput}}
			var logBuffer *logging.Buffer
			if tt.existingBuffer {
				logBuffer = logging.NewBuffer(1)
			}

			configureAgentCommandLogging(&appConfig, &logBuffer, tt.logView, tt.logOutputChanged)

			if appConfig.Log.Output != tt.wantOutput {
				t.Fatalf("log output = %q, want %q", appConfig.Log.Output, tt.wantOutput)
			}
			if gotBuffer := logBuffer != nil; gotBuffer != tt.wantBuffer {
				t.Fatalf("log buffer present = %v, want %v", gotBuffer, tt.wantBuffer)
			}
		})
	}
}

func readSetupConfig(t *testing.T, path string) config.AppConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}
