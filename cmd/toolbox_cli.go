package cmd

import (
	"fmt"
	"io"
	"strings"

	"charm.land/huh/v2"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/system/toolbox"
	"github.com/spf13/cobra"
)

func NewToolboxCmd(app *config.AppConfig) *cobra.Command {
	root := &cobra.Command{Use: "toolbox", Short: "Install toolbox tools"}
	root.AddCommand(&cobra.Command{
		Use: "install [term]", Short: "Search and install tools from " + toolbox.HubRepo, Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return installTools(cmd, *app, strings.Join(args, "")) },
	})
	root.AddCommand(newToolboxAuthCmd(app))
	return root
}

// newToolboxAuthCmd writes a tool's owner-only .secrets.json, from the GitHub
// CLI login or from stdin, so the token never lands in argv or shell history.
func newToolboxAuthCmd(app *config.AppConfig) *cobra.Command {
	var fromGH bool
	cmd := &cobra.Command{
		Use:   "auth <tool>",
		Short: "Store a tool's token in its .secrets.json, from gh or stdin",
		Long:  "  invokr toolbox auth github_pr --from-gh          invokr toolbox auth github_pr < token.txt",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := readToken(cmd, fromGH)
			if err != nil {
				return err
			}
			path, err := toolbox.WriteToolSecret(app.Toolbox.Dir, args[0], token)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "stored token for %s in %s\n", args[0], path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromGH, "from-gh", false, "copy the token from gh auth token")
	return cmd
}

// readToken returns the token to store: the gh login token when asked, otherwise
// the trimmed token piped on stdin.
func readToken(cmd *cobra.Command, fromGH bool) (string, error) {
	if fromGH {
		return toolbox.GHToken(cmd.Context())
	}
	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	if token := strings.TrimSpace(string(raw)); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("no token on stdin (or pass --from-gh)")
}

func installTools(cmd *cobra.Command, app config.AppConfig, term string) error {
	found, err := toolbox.HubTools(cmd.Context(), app)
	if err != nil {
		return err
	}
	var opts []huh.Option[int]
	for i, tool := range found {
		if strings.Contains(tool.Name, term) {
			opts = append(opts, huh.NewOption(tool.Name+" — "+tool.Description, i))
		}
	}
	var picked []int
	if err := huh.NewForm(huh.NewGroup(huh.NewMultiSelect[int]().Title("Install tools (space selects, enter confirms)").Options(opts...).Filterable(true).Value(&picked))).Run(); err != nil {
		return err
	}
	if len(picked) == 0 {
		return fmt.Errorf("nothing selected: press space on a tool before enter")
	}
	for _, i := range picked {
		approval := found[i].Approval
		if _, err := runPromptField(huh.NewSelect[string]().Title(found[i].Name+": approval in Auto mode").Options(huh.NewOptions(toolbox.HubApprovals...)...).Value(&approval), &approval); err != nil {
			return err
		}
		if err := toolbox.InstallHubTool(app, found[i], approval); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "installed", found[i].Name)
	}
	return nil
}
