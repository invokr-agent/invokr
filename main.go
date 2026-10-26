package main

import (
	"context"
	"fmt"
	"github.com/invokr-ai/invokr/platform/version"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/invokr-ai/invokr/capability/docker_sandbox"
	"github.com/invokr-ai/invokr/cmd"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/setup"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) (err error) {
	// The tokf filter helper must write nothing but the copied payload: any
	// startup log line (config warnings land on stderr, which tokf merges into
	// its output) would corrupt the tool result it is filtering. Handle it
	// before config load so the process cannot emit anything else.
	if len(os.Args) > 1 && os.Args[1] == "tokf-filter-stdin" {
		_, err = io.Copy(os.Stdout, os.Stdin)
		return err
	}

	cfg, err := loadStartupConfig()
	if err != nil {
		return err
	}

	if err := cmd.ConfigureStartupModes(&cfg, os.Args[1:]); err != nil {
		return err
	}
	version.Edge = cfg.UpdateChannel == "edge"
	if handled, exitCode, sandboxErr := docker_sandbox.MaybeRelaunch(cfg.Sandbox, cfg.Provider.BaseURL, os.Args); handled {
		if sandboxErr != nil {
			return sandboxErr
		}
		os.Exit(exitCode)
	}

	rootCmd, cleanup := cmd.NewRootCmd(cfg)
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil && err == nil {
			err = cleanupErr
		}
	}()
	return rootCmd.ExecuteContext(ctx)
}

func loadStartupConfig() (config.AppConfig, error) {
	choice, err := parseStartupChoice(os.Args)
	if err != nil {
		return config.AppConfig{}, err
	}
	// `setup` owns its home/config creation, including the questionnaire and
	// prefill check. Creating it here first would turn a first-run setup into
	// an "already configured" run before the questionnaire can decide that.
	if choice.configUse {
		if _, err := setup.Ensure(); err != nil {
			return config.AppConfig{}, err
		}
	} else if !choice.setup {
		if _, err := setup.EnsureFirstRun(os.Stdin, os.Stdout); err != nil {
			return config.AppConfig{}, err
		}
	}
	if choice.setup || choice.configUse {
		return config.LoadDefaultFileWithoutProfile()
	}
	return config.LoadDefaultFileWithProfile(choice.profile)
}

type startupChoice struct {
	profile          string
	setup, configUse bool
}

// parseStartupChoice uses Cobra's command and flag parser before config load,
// so prompt text after -- cannot change the selected profile or recovery path.
func parseStartupChoice(args []string) (startupChoice, error) {
	root, cleanup := cmd.NewRootCmd(config.AppConfig{})
	defer func() { _ = cleanup() }()
	if len(args) > 0 {
		args = args[1:]
	}
	command, remaining, err := root.Find(args)
	if err != nil {
		return startupChoice{}, err
	}
	command.InitDefaultHelpFlag()
	command.InitDefaultVersionFlag()
	if err := command.ParseFlags(remaining); err != nil {
		return startupChoice{}, err
	}
	profile, _ := command.Flags().GetString("profile")
	parent := command.Parent()
	return startupChoice{profile: profile, setup: command.Name() == "setup",
		configUse: command.Name() == "use" && parent != nil && parent.Name() == "config"}, nil
}
