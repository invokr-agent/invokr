package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

const relayInstallHint = "curl -fsSL https://raw.githubusercontent.com/invokr-agent/agent-relay/main/install.sh | sh"

// NewRelayCmd drives agent-relay's background service; invokr itself keeps nothing running.
func NewRelayCmd() *cobra.Command {
	relay := &cobra.Command{
		Use:     "relay",
		Short:   "Start, stop or check the agent-relay chat service",
		Long:    "Runs agent-relay (Telegram, Signal, Matrix) as a background user service that\ndrives `invokr acp`. Configure it first with `agent-relay setup`.",
		GroupID: groupServe,
	}
	for _, action := range []struct{ use, short, service string }{
		{"start", "Install and start the relay service", "install"},
		{"stop", "Stop and remove the relay service", "uninstall"},
		{"status", "Show the relay service status", "status"},
	} {
		relay.AddCommand(&cobra.Command{
			Use: action.use, Short: action.short, Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error { return runRelayService(cmd, action.service) },
		})
	}
	return relay
}

func runRelayService(cmd *cobra.Command, action string) error {
	bin, err := exec.LookPath("agent-relay")
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("agent-relay is not installed; install it with:\n  %s\nthen run `agent-relay setup`", relayInstallHint)
	}
	if err != nil {
		return err
	}
	c := exec.CommandContext(cmd.Context(), bin, "service", action)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
	return c.Run()
}
