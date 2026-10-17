package cmd

import (
	"io"

	"github.com/spf13/cobra"
)

func NewTokfFilterStdinCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "tokf-filter-stdin",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := io.Copy(cmd.OutOrStdout(), cmd.InOrStdin())
			return err
		},
	}

	cmd.Flags().String("tool", "", "Tool name for tokf filter matching")
	cmd.Flags().String("source", "", "Tool source for tokf filter matching")
	cmd.Flags().String("runtime", "", "Tool runtime for tokf filter matching")

	return cmd
}
