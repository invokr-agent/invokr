package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/setup"

	"github.com/spf13/cobra"
)

func NewSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "setup",
		Short:   "Configure Invokr (first-run questionnaire)",
		Long:    "Asks where your model lives and what to enable, then writes\n~/.invokr/config.json. Safe to re-run; `invokr config` shows the\nresult and `invokr doctor` checks it.",
		Example: "  invokr setup",
		Args:    cobra.NoArgs,
		GroupID: groupConfigure,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Scripted readers (tests) are fine; a piped or closed real stdin is not.
			if _, isFile := cmd.InOrStdin().(*os.File); isFile && !isTerminalInput(cmd) {
				return errors.New("setup needs an interactive terminal; pass --base-url/--model or edit ~/.invokr/config.json")
			}
			// --base-url or INVOKR_BASE_URL prefill the URL question rather than
			// being ignored for the built-in default.
			baseURL := config.Env("INVOKR_BASE_URL")
			if flag := cmd.Flag("base-url"); flag != nil && flag.Changed {
				baseURL = flag.Value.String()
			}
			result, err := setup.RunQuestionnaireFrom(cmd.InOrStdin(), cmd.OutOrStdout(), baseURL)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out)
			_, _ = fmt.Fprintf(out, "Invokr home: %s\n", result.HomeDir)
			_, _ = fmt.Fprintf(out, "Config: %s\n", result.ConfigPath)
			_, _ = fmt.Fprintf(out, "Persona (system prompt): %s\n", result.PersonaPath)
			_, _ = fmt.Fprintf(out, "JSON profiles: %s\n", setup.ToolTemplatesDir(result.HomeDir))
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
