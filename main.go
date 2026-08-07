package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strings"
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
		log.Fatal(err)
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
	ensure := setup.Ensure
	if !setupRequested(os.Args) {
		// A missing ~/.invokr means a fresh install: ask instead of guessing.
		ensure = func() (setup.Result, error) { return setup.EnsureFirstRun(os.Stdin, os.Stdout) }
	}
	if _, err := ensure(); err != nil {
		return config.AppConfig{}, err
	}
	return config.LoadDefaultFileWithProfile(profileRequested(os.Args))
}

// profileRequested scrapes --profile from the raw arguments. It runs before
// cobra parses anything because the profile has to be overlaid onto the config
// before flags bind to it — that ordering is what lets an explicit flag still
// beat a profile value. Cobra later accepts the flag itself (see
// registerRootFlags) but its parsed value is unused.
func profileRequested(args []string) string {
	rest := args[1:]
	for i, arg := range rest {
		if arg == "--profile" {
			if i+1 < len(rest) {
				return rest[i+1]
			}
			return ""
		}
		if name, ok := strings.CutPrefix(arg, "--profile="); ok {
			return name
		}
	}
	return ""
}

// setupRequested reports whether the user invoked the setup command itself, in
// which case the preflight must not prompt as well.
func setupRequested(args []string) bool {
	return slices.ContainsFunc(args[1:], func(arg string) bool { return arg == "setup" || arg == "init" })
}
