package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/setup"

	"github.com/spf13/cobra"
)

func NewSetupCmd() *cobra.Command {
	setupCmd := &cobra.Command{
		Use:     "setup",
		Short:   "Configure Invokr (first-run questionnaire)",
		Long:    "Asks where your model lives and what to enable, then writes\n~/.invokr/config.json. Safe to re-run; `invokr config` shows the\nresult and `invokr doctor` checks it.\n\n`setup git-identity` is a step of its own: it configures the git and GitHub\naccount invokr commits as, without the questionnaire.",
		Example: "  invokr setup\n  invokr setup git-identity\n  invokr setup git-identity --reset",
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
			result, err := setup.RunQuestionnaireFromExisting(cmd.InOrStdin(), cmd.OutOrStdout(), baseURL)
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
	setupCmd.AddCommand(newGitIdentityCmd())
	return setupCmd
}

// newGitIdentityCmd configures or clears invokr's own git/GitHub identity. It
// runs one step, so the identity can be set or changed without re-answering
// the questionnaire, and --reset leaves nothing stored: invokr then uses the
// host's git and gh, with the host's own config untouched.
func newGitIdentityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "git-identity",
		Short:   "Set the git/GitHub identity invokr commits as (--reset clears it)",
		Long:    "Asks for the name, email and optional SSH signing key invokr should\nuse, and stores them under the Invokr home. Every git and gh child then\ninherits them through the environment, which outranks a repository's own\nuser.email, so a repo that pins an identity is still committed as this account.\nThe choice is explicit: only this command writes it, and nothing the agent\nruns can. `--reset` removes the stored identity, its signing settings and the\nInvokr gh login.",
		Example: "  invokr setup git-identity\n  invokr setup git-identity --reset",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			homeDir := config.InvokrHomeDir()
			if reset, _ := cmd.Flags().GetBool("reset"); reset {
				if err := config.ClearGitIdentity(homeDir); err != nil {
					return err
				}
			} else if _, err := setup.RunGitIdentityStep(cmd.InOrStdin(), cmd.OutOrStdout(), homeDir); err != nil {
				return err
			}
			// renderIdentity shows the identity this run applies, with resolved paths.
			applied, err := config.ApplyGitIdentity(homeDir)
			if err != nil {
				return err
			}
			return renderIdentity(cmd.OutOrStdout(), applied, homeDir)
		},
	}
	cmd.Flags().Bool("reset", false, "Clear the stored identity, its signing settings and the Invokr gh login")
	return cmd
}

// renderIdentity says which account invokr will commit and push as, or that it
// falls back to the host's git. The gh config dir is shown because that is
// where `gh auth login` put the bot's credentials.
func renderIdentity(out io.Writer, identity config.GitIdentityConfig, homeDir string) error {
	if identity.Name == "" || identity.Email == "" {
		_, err := fmt.Fprintf(out, "git identity: host's (nothing stored under %s)\n", homeDir)
		return err
	}
	_, err := fmt.Fprintf(out, "git identity: %s <%s>\nsigning key: %q\ngh login dir: %s\n",
		identity.Name, identity.Email, identity.SigningKey, config.GitIdentityDir(homeDir))
	return err
}
