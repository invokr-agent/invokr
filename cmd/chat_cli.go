package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/invokr-agent/invokr/surface/acpagent"
)

func NewACPCmd(services ServicesProvider) *cobra.Command {
	return &cobra.Command{
		Use:          "acp",
		Short:        "Run the agent as an ACP (Agent Client Protocol) agent over stdio",
		GroupID:      groupServe,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := services()
			if err != nil {
				return err
			}
			if s.App.Log.Output == "stdout" {
				return fmt.Errorf("acp: stdout carries the protocol; set --log-output to stderr or a file")
			}
			protocol := cmd.OutOrStdout()
			os.Stdout = os.Stderr
			return acpagent.Serve(cmd.Context(), cmd.InOrStdin(), protocol, s.NewChatSession)
		},
	}
}
