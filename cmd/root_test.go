package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/cmd/agentui"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/runtime"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

type cleanupSpyRuntime struct {
	cleanupCalls int
}

func TestRootFlagOverridesLoadedEnvironment(t *testing.T) {
	t.Setenv("INVOKR_MODEL", "env-model")
	t.Setenv("INVOKR_HOME", t.TempDir())
	app, err := config.LoadDefaultFileWithProfile("")
	if err != nil {
		t.Fatal(err)
	}
	spy := &cleanupSpyRuntime{}
	var bootstrapped config.AppConfig
	root, _ := newRootCmd(app, func(app config.AppConfig, _ runtime.Options) (commandRuntime, error) {
		bootstrapped = app
		return spy, nil
	})
	root.AddCommand(&cobra.Command{Use: "capture", Run: func(*cobra.Command, []string) {}})
	root.SetArgs([]string{"--model", "flag-model", "capture"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if bootstrapped.Provider.Model != "flag-model" {
		t.Fatalf("bootstrapped model = %q, want flag-model", bootstrapped.Provider.Model)
	}
}

func TestConfigShowPrintsEffectiveRedactedConfigWithoutBootstrap(t *testing.T) {
	app := config.AppConfig{
		Provider:  config.ProviderConfig{APIKey: "provider-secret-1234"},
		Messaging: config.MessagingConfig{Telegram: config.TelegramConfig{Token: "telegram-secret-5678"}},
	}
	bootstrapCalls := 0
	root, _ := newRootCmd(app, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		bootstrapCalls++
		return &cleanupSpyRuntime{}, nil
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--model", "flag-model", "config", "show"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if bootstrapCalls != 0 {
		t.Fatalf("runtime bootstrap calls = %d, want 0", bootstrapCalls)
	}
	shown := out.String()
	for _, secret := range []string{"provider-secret-1234", "telegram-secret-5678"} {
		if strings.Contains(shown, secret) {
			t.Fatalf("config output leaked %q: %s", secret, shown)
		}
	}
	for _, want := range []string{`"model": "flag-model"`, `"api_key": "****1234"`, `"token": "****5678"`} {
		if !strings.Contains(shown, want) {
			t.Fatalf("config output missing %q: %s", want, shown)
		}
	}
}

func (r *cleanupSpyRuntime) Services() runtime.Services {
	return runtime.Services{Logger: zap.NewNop()}
}

func (r *cleanupSpyRuntime) Cleanup() error {
	r.cleanupCalls++
	return nil
}

func TestRootCleansRuntimeAfterCommandFailure(t *testing.T) {
	spy := &cleanupSpyRuntime{}
	root, cleanup := newRootCmd(config.AppConfig{}, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		return spy, nil
	})
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.AddCommand(&cobra.Command{
		Use: "fail",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("command failed")
		},
	})
	root.SetArgs([]string{"fail"})

	if err := root.Execute(); err == nil {
		t.Fatal("expected command failure")
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup returned error: %v", err)
	}
	if spy.cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", spy.cleanupCalls)
	}
}

func TestRootBareInvocationRunsAgent(t *testing.T) {
	orig := runAgentUI
	called := false
	runAgentUI = func(context.Context, agentui.Params) error {
		called = true
		return nil
	}
	defer func() { runAgentUI = orig }()
	root, _ := newRootCmd(config.AppConfig{}, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		return &cleanupSpyRuntime{}, nil
	})
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatalf("bare invocation failed: %v", err)
	}
	if !called {
		t.Fatal("bare root invocation did not run the agent UI")
	}
}

// The bare root invocation borrows agentCmd.RunE, which closes over the resume
// flag variables. Root must expose those same flags, or `invokr --resume` dies
// with "unknown flag" before the resume path is ever reached.
func TestRootBareInvocationAcceptsResumeFlag(t *testing.T) {
	orig := runAgentUI
	runAgentUI = func(context.Context, agentui.Params) error { return nil }
	defer func() { runAgentUI = orig }()

	if root, _ := newRootCmd(config.AppConfig{}, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		return &cleanupSpyRuntime{}, nil
	}); root.Flags().Lookup("resume") == nil {
		t.Fatal("root command is missing the --resume flag")
	}

	root, _ := newRootCmd(config.AppConfig{}, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		return &cleanupSpyRuntime{}, nil
	})
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs([]string{"--resume", "no-such-session-id"})

	// Reaching the store proves root's flag is bound to the same variable the
	// borrowed RunE reads, not merely registered.
	err := root.Execute()
	if err == nil {
		t.Fatal("--resume with an unknown session ID should fail")
	}
	if !strings.Contains(err.Error(), "no-such-session-id") {
		t.Fatalf("error = %v, want it to reference the requested session ID", err)
	}
}

func TestRootVersionFlagPrintsWithoutBootstrap(t *testing.T) {
	bootstrapCalls := 0
	root, _ := newRootCmd(config.AppConfig{}, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		bootstrapCalls++
		return &cleanupSpyRuntime{}, nil
	})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	if bootstrapCalls != 0 {
		t.Fatalf("bootstrap calls = %d, want 0 (--version must not bootstrap the runtime)", bootstrapCalls)
	}
	if !strings.HasPrefix(out.String(), "invokr ") {
		t.Fatalf("--version output = %q, want prefix %q", out.String(), "invokr ")
	}
}

func TestRootRejectsUnknownSubcommand(t *testing.T) {
	root, _ := newRootCmd(config.AppConfig{}, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		return &cleanupSpyRuntime{}, nil
	})
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs([]string{"agnet"})
	if err := root.Execute(); err == nil {
		t.Fatal("unknown subcommand should error, not launch the agent")
	}
}

func TestRootExecuteContextPropagatesToSubcommands(t *testing.T) {
	spy := &cleanupSpyRuntime{}
	root, _ := newRootCmd(config.AppConfig{}, func(config.AppConfig, runtime.Options) (commandRuntime, error) {
		return spy, nil
	})
	root.SilenceErrors = true
	root.SilenceUsage = true

	type ctxKey struct{}
	want := context.WithValue(context.Background(), ctxKey{}, "sentinel")

	var gotVal any
	root.AddCommand(&cobra.Command{
		Use: "echo-ctx",
		RunE: func(cmd *cobra.Command, _ []string) error {
			gotVal = cmd.Context().Value(ctxKey{})
			return nil
		},
	})
	root.SetArgs([]string{"echo-ctx"})

	if err := root.ExecuteContext(want); err != nil {
		t.Fatalf("ExecuteContext returned error: %v", err)
	}
	if gotVal != "sentinel" {
		t.Fatalf("subcommand context value = %v, want sentinel", gotVal)
	}
}

func TestBaseURLFlagDisablesRouterTable(t *testing.T) {
	app := config.AppConfig{
		Provider: config.ProviderConfig{
			Router: config.RouterConfig{
				Active: "local",
				Providers: map[string]config.RouterProviderConfig{
					"local": {Type: "openai-compatible", BaseURL: "http://configured:8080"},
				},
				Profiles: map[string]config.RouterProfileConfig{
					"local": {Provider: "local", Model: "configured-model"},
				},
			},
		},
	}
	spy := &cleanupSpyRuntime{}
	var bootstrapped config.AppConfig
	root, _ := newRootCmd(app, func(app config.AppConfig, _ runtime.Options) (commandRuntime, error) {
		bootstrapped = app
		return spy, nil
	})
	root.AddCommand(&cobra.Command{Use: "capture", Run: func(*cobra.Command, []string) {}})
	root.SetArgs([]string{"--base-url", "http://mock:9999", "capture"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if bootstrapped.Provider.Router.Enabled() {
		t.Fatalf("router table still enabled after explicit --base-url override: %+v", bootstrapped.Provider.Router)
	}
	if bootstrapped.Provider.BaseURL != "http://mock:9999" {
		t.Fatalf("base URL = %q, want the flag value", bootstrapped.Provider.BaseURL)
	}
}

func TestNoBaseURLFlagKeepsRouterTable(t *testing.T) {
	app := config.AppConfig{
		Provider: config.ProviderConfig{
			Router: config.RouterConfig{
				Active: "local",
				Providers: map[string]config.RouterProviderConfig{
					"local": {Type: "openai-compatible", BaseURL: "http://configured:8080"},
				},
				Profiles: map[string]config.RouterProfileConfig{
					"local": {Provider: "local", Model: "configured-model"},
				},
			},
		},
	}
	spy := &cleanupSpyRuntime{}
	var bootstrapped config.AppConfig
	root, _ := newRootCmd(app, func(app config.AppConfig, _ runtime.Options) (commandRuntime, error) {
		bootstrapped = app
		return spy, nil
	})
	root.AddCommand(&cobra.Command{Use: "capture", Run: func(*cobra.Command, []string) {}})
	root.SetArgs([]string{"capture"})

	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bootstrapped.Provider.Router.Enabled() {
		t.Fatalf("router table was cleared without a --base-url override")
	}
}

// --allow-all is auto-approve-everything, so it must also lift the shell's
// executable allowlist; without the flag the allowlist stays in force.
func TestAllowAllLiftsShellExecutableAllowlist(t *testing.T) {
	run := func(args ...string) config.AppConfig {
		t.Helper()
		var bootstrapped config.AppConfig
		root, _ := newRootCmd(config.AppConfig{}, func(app config.AppConfig, _ runtime.Options) (commandRuntime, error) {
			bootstrapped = app
			return &cleanupSpyRuntime{}, nil
		})
		// A stand-in for `exec`, carrying its real flags: the agent run
		// itself needs a live runtime, and only the config handed to
		// bootstrap is under test.
		capture := &cobra.Command{Use: "capture", Run: func(*cobra.Command, []string) {}}
		registerExecFlags(capture, &execOptions{}, &config.AppConfig{})
		root.AddCommand(capture)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		return bootstrapped
	}
	if run("capture").Shell.AllowAnyCommand {
		t.Fatal("allowlist lifted on a command with no --allow-all")
	}
	if !run("capture", "--allow-all").Shell.AllowAnyCommand {
		t.Fatal("--allow-all did not lift the shell executable allowlist")
	}
}
