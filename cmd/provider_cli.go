package cmd

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/term"

	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"

	"github.com/spf13/cobra"
)

// installableProviders are the provider types that run as their own process and
// therefore need a binary. "openai-compatible" is absent deliberately: it is
// served in-process and needs nothing installed.
var installableProviders = []string{"anthropic", "bedrock", "openai"}

// defaultProviderModels seeds a new profile's model. A provider absent here is
// prompted for rather than guessed at.
var defaultProviderModels = map[string]string{"anthropic": constants.DefaultAnthropicModel}

func NewProviderCmd() *cobra.Command {
	providerCmd := &cobra.Command{
		Use:     "provider",
		Aliases: []string{"providers"},
		Short:   "Install model providers and store their API keys",
		Long:    "A provider is what actually talks to a model vendor. Anthropic, Bedrock\nand OpenAI run as their own process, downloaded by `install`;\nopenai-compatible endpoints need nothing installed and are served\nin-process.\n\nInstalling one also defines a profile for it — `invokr config profiles`\nlists them, `invokr config use` switches between them.",
		Example: "  invokr provider install anthropic\n  printf %s \"$ANTHROPIC_API_KEY\" | invokr provider set-key anthropic\n  invokr config use anthropic",
		GroupID: groupConfigure,
	}
	providerCmd.AddCommand(newProviderInstallCmd(), newProviderKeyCmd(), retiredCommand("key", "invokr provider set-key"))
	return providerCmd
}

// newProviderKeyCmd stores an API key on a provider definition. The key is read
// from stdin rather than an argument, so it does not land in shell history or
// the process table.
func newProviderKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-key <provider>",
		Short: "Store a provider's API key, read from stdin",
		Long:  "Reads the key from stdin and writes it to provider.router.providers.<provider>.api_key,\nreplacing any api_key_env reference. The key is stored in the config file in plain\ntext; to keep it out of the file, set api_key_env instead and export the variable.\n\n  invokr provider set-key anthropic < key.txt\n  printf %s \"$ANTHROPIC_API_KEY\" | invokr provider set-key anthropic",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("read key from stdin: %w", err)
			}
			key := strings.TrimSpace(string(raw))
			if key == "" {
				return fmt.Errorf("no key on stdin")
			}
			path := config.DefaultConfigPath()
			if err := config.SetProviderAPIKey(path, args[0], key); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "stored key for provider %s in %s\n", args[0], path)
			return nil
		},
	}
}

// newProviderInstallCmd downloads a signed provider binary from the router's
// release registry.
//
// It used to build one with 'go install', because the managed installer needed
// a trust root invokr did not have. It has one now: the registry's public key
// is not secret, and naming it turns installation into a verified download that
// needs no Go toolchain on the user's machine.
func newProviderInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "install [type]",
		Short:     "Download a provider process and store its API key",
		Long:      "Downloads go-inference-router-provider-<type> from the signed release\nregistry, then prompts for the provider's API key and stores it in the\nconfig.\n\nNo Go toolchain is needed. The manifest is verified against the release\nsigning key, the download must match the size and SHA-256 it names, and\nthe digest is checked again every time the provider is launched. The\nbinary lands in the router's cache, not on PATH.\n\nThe newest stable release is installed: the registry describes the\ncurrent release's artifacts, and compatibility is carried by the\nprotocol the binary speaks rather than by matching version numbers.\n\nOn a terminal, the provider type and key are prompted for when not supplied.\nWithout a terminal only the install runs, so scripts stay non-interactive;\nuse 'invokr provider set-key' to store a key from a pipe.",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: providerTypes(),
		RunE: func(cmd *cobra.Command, args []string) error {
			providerType := ""
			if len(args) == 1 {
				providerType = args[0]
			}
			return runProviderInstall(cmd, providerType)
		},
	}
}

// runProviderInstall walks the whole setup for one provider: pick it, install
// it, store its key, and offer to switch to it. Each step is skipped when it
// cannot run, so the command degrades to a plain install rather than failing.
func runProviderInstall(cmd *cobra.Command, providerType string) error {
	interactive := isInteractive(cmd)
	if providerType == "" {
		if !interactive {
			return fmt.Errorf("provider type is required without a terminal; one of: %s", strings.Join(providerTypes(), ", "))
		}
		var err error
		if providerType, err = selectProviderType(); err != nil {
			return err
		}
	}
	if err := installProvider(cmd, providerType); err != nil {
		return err
	}
	if !interactive {
		return nil
	}
	return storeKeyInteractively(cmd, providerType)
}

// storeKeyInteractively prompts for a key and writes it to the config. An empty
// key is a deliberate skip: the provider may authenticate from the environment
// via api_key_env, which the install itself does not depend on.
func storeKeyInteractively(cmd *cobra.Command, providerType string) error {
	out := cmd.OutOrStdout()
	key, err := promptAPIKey(providerType)
	if err != nil {
		return err
	}
	if key == "" {
		_, _ = fmt.Fprintf(out, "no key entered; set one later with 'invokr provider set-key %s'\n", providerType)
		return nil
	}
	model, err := resolveProviderModel(providerType)
	if err != nil {
		return err
	}
	path := config.DefaultConfigPath()
	// A config written before the router table existed has nowhere to put a
	// provider, so define one rather than sending the user to edit JSON.
	if err := config.EnsureRouterProvider(path, providerType, providerType, model); err != nil {
		_, _ = fmt.Fprintf(out, "installed, but the config was not updated: %v\n", err)
		return nil
	}
	if err := config.SetProviderAPIKey(path, providerType, key); err != nil {
		_, _ = fmt.Fprintf(out, "installed, but the key was not stored: %v\n", err)
		return nil
	}
	_, _ = fmt.Fprintf(out, "stored key for provider %s in %s\n", providerType, path)
	_, _ = fmt.Fprintf(out, "profile %q now serves %s; switch with 'invokr config use %s'\n", providerType, model, providerType)
	return nil
}

// resolveProviderModel picks the model a new profile starts with, asking when
// there is no sensible default to offer.
func resolveProviderModel(providerType string) (string, error) {
	if model := defaultProviderModels[providerType]; model != "" {
		return model, nil
	}
	var model string
	return runPromptField(huh.NewInput().Title(fmt.Sprintf("Which %s model should the profile use?", providerType)).
		Description("Written to the profile; change it any time in the config.").Value(&model), &model)
}

// runPromptField runs one huh field as a single-question form and returns the
// trimmed answer it wrote into value.
func runPromptField(field huh.Field, value *string) (string, error) {
	if err := huh.NewForm(huh.NewGroup(field)).Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(*value), nil
}

// isInteractive reports whether prompting is possible. Cobra's streams may be
// redirected by tests, so both the real stdin and the command's own output must
// be terminals.
func isInteractive(cmd *cobra.Command) bool {
	return cmd.InOrStdin() == os.Stdin && term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

func selectProviderType() (string, error) {
	var selected string
	return runPromptField(huh.NewSelect[string]().Title("Which provider?").
		Description("openai-compatible providers need no binary; they run in-process.").Options(huh.NewOptions(providerTypes()...)...).Value(&selected), &selected)
}

func promptAPIKey(providerType string) (string, error) {
	var key string
	return runPromptField(huh.NewInput().Title(fmt.Sprintf("API key for %s", providerType)).Description("Stored in the config file. Leave empty to skip.").EchoMode(huh.EchoModePassword).
		Value(&key), &key)
}

func providerTypes() []string { return slices.Clone(installableProviders) }

// installProvider downloads a verified provider binary from the release
// registry into the router's install cache, where the router resolves it by
// digest rather than by name on PATH.
func installProvider(cmd *cobra.Command, providerType string) error {
	if !slices.Contains(installableProviders, providerType) {
		return fmt.Errorf("unknown provider type %q; known types: %s", providerType, strings.Join(providerTypes(), ", "))
	}
	out := cmd.OutOrStdout()
	path, err := clients.InstallProvider(cmd.Context(), providerType, func(plan clients.InstallPlan) error {
		// Printed before anything is downloaded: this is the point at which
		// the size and digest can still inform a decision.
		_, _ = fmt.Fprintf(out, "downloading %s %s (%d bytes)\n", plan.Provider, plan.Version, plan.Size)
		_, _ = fmt.Fprintf(out, "  from   %s\n", plan.Source)
		_, _ = fmt.Fprintf(out, "  sha256 %s\n", plan.SHA256)
		return nil
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "installed %s\n", path)
	return nil
}
