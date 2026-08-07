package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/logging"

	"github.com/stretchr/testify/require"
)

func TestSetupCmdUsesQuestionnaireDefaults(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)

	cmd := NewSetupCmd()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader("\n\n\n\n"))
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
}

func TestSetupCmdUsesCustomAnswers(t *testing.T) {
	home := filepath.Join(t.TempDir(), "invokr-home")

	cmd := NewSetupCmd()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader("http://provider.local:9090\n" + home + "\nno\ncatppuccin-mocha\n"))
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
	root.SetIn(strings.NewReader("\n\n\n\n"))
	root.SetOut(&out)
	root.SetArgs([]string{"setup"})

	if err := root.Execute(); err != nil {
		t.Fatalf("root setup command returned error: %v", err)
	}
}

func TestRootCmdIncludesLogViewFlag(t *testing.T) {
	root, _ := NewRootCmd(config.AppConfig{})

	flag := root.PersistentFlags().Lookup("log-view")
	require.NotNil(t, flag, "expected log-view flag")
	if flag.DefValue != "false" {
		t.Fatalf("log-view default = %q, want false", flag.DefValue)
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
