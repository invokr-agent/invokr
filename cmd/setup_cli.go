package cmd

import (
	"fmt"

	"github.com/invokr-ai/invokr/platform/setup"

	"github.com/spf13/cobra"
)

func NewSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "setup",
		Aliases: []string{"init"},
		Short:   "Configure Invokr (first-run questionnaire)",
		Long:    "Asks where your model lives and what to enable, then writes\n~/.invokr/config.json. Safe to re-run; `invokr config` shows the\nresult and `invokr doctor` checks it.",
		Example: "  invokr setup",
		Args:    cobra.NoArgs,
		GroupID: groupConfigure,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := setup.RunQuestionnaire(cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out)
			_, _ = fmt.Fprintf(out, "Invokr home: %s\n", result.HomeDir)
			_, _ = fmt.Fprintf(out, "Config: %s\n", result.ConfigPath)
			_, _ = fmt.Fprintf(out, "Persona (system prompt): %s\n", result.PersonaPath)
			_, _ = fmt.Fprintf(out, "LLM provider base_url: %s\n", result.ProviderBaseURL)
			if result.Model != "" {
				_, _ = fmt.Fprintf(out, "Default model: %s\n", result.Model)
			}
			if result.MemoryEnabled {
				_, _ = fmt.Fprintln(out, "Built-in memory: enabled (Zerfoo models download on first use)")
			}
			return nil
		},
	}
}
