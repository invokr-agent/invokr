package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

const relayInstallHint = "curl -fsSL https://raw.githubusercontent.com/invokr-agent/agent-relay/main/install.sh | sh"

// NewRelayCmd drives agent-relay in the background; invokr itself keeps nothing running.
func NewRelayCmd() *cobra.Command {
	relay := &cobra.Command{
		Use:     "relay",
		Short:   "Start, stop or check the agent-relay chat relay",
		Long:    "Runs agent-relay (Telegram, Signal, Matrix) in the background, driving\n`invokr acp` in the current directory. Configure it first with `agent-relay setup`.\nOnly one relay runs per user.",
		GroupID: groupServe,
	}
	var replace bool
	start := &cobra.Command{
		Use: "start", Short: "Start the relay in the background for the current directory", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			args := []string{"start", "-cwd", wd}
			if replace {
				args = append(args, "-replace")
			}
			return runAgentRelay(cmd, args...)
		},
	}
	start.Flags().BoolVar(&replace, "replace", false, "Stop the running relay first")
	relay.AddCommand(start,
		&cobra.Command{Use: "stop", Short: "Stop the relay", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error { return runAgentRelay(cmd, "stop") }},
		&cobra.Command{Use: "status", Short: "Show whether the relay runs, and for which directory", Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error { return runAgentRelay(cmd, "status") }},
		&cobra.Command{Use: "service install|uninstall|status|restart", Short: "Manage the relay as a login service that restarts on failure", Args: cobra.ExactArgs(1), SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error { return runAgentRelay(cmd, "service", args[0]) }},
	)
	return relay
}

func runAgentRelay(cmd *cobra.Command, args ...string) error {
	bin, err := exec.LookPath("agent-relay")
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("agent-relay is not installed; install it with:\n  %s\nthen run `agent-relay setup`", relayInstallHint)
	}
	if err != nil {
		return err
	}
	// Not CommandContext: ctrl+c reaches agent-relay through the terminal, and
	// killing it mid-start would leave the relay it spawned unreported.
	c := exec.Command(bin, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
	err = c.Run()
	// agent-relay has already said what went wrong: pass its exit code on silently.
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		os.Exit(exitErr.ExitCode())
	}
	return err
}
