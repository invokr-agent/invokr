package cmd

import (
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/system/toolbox"
	"github.com/spf13/cobra"
)

// NewToolboxCmd searches and installs tools from the toolbox hub repository.
func NewToolboxCmd(app *config.AppConfig) *cobra.Command {
	root := &cobra.Command{Use: "toolbox", Short: "Install toolbox tools"}
	root.AddCommand(&cobra.Command{
		Use: "install [term]", Short: "Search and install tools from " + toolbox.HubRepo, Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return installTools(cmd, *app, strings.Join(args, "")) },
	})
	return root
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
