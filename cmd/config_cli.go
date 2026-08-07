package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/invokr-ai/invokr/config"

	"github.com/spf13/cobra"
)

func NewConfigCmd(app *config.AppConfig) *cobra.Command {
	configCmd := &cobra.Command{
		Use:     "config",
		Short:   "Show effective configuration and switch provider profiles",
		Long:    "The effective config is what Invokr actually runs with: the file at\n~/.invokr/config.json, overlaid with INVOKR_* environment variables\nand the flags on this invocation. Run bare, it prints that, secrets\nredacted.\n\nA profile names one provider plus one model. `profiles` lists them and\n`use` switches which is active, writing the choice back to the config\nfile so it outlives the process.",
		Example: "  invokr config                  # effective config, secrets redacted\n  invokr config profiles         # configured profiles, active one marked *\n  invokr config use anthropic",
		GroupID: groupConfigure,
	}
	configCmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print effective configuration with secrets redacted",
		Long:  "File, then INVOKR_* environment overrides, then this invocation's flags — as JSON.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(app.Redacted())
		},
	})
	configCmd.AddCommand(newProfilesCmd(app), newUseProfileCmd(app))
	useDefaultSubcommand(configCmd, "show")
	return configCmd
}

// newProfilesCmd lists the configured provider profiles, marking the active
// one, so a user can see what "config use" accepts.
func newProfilesCmd(app *config.AppConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "profiles",
		Aliases: []string{"profile"},
		Short:   "List configured provider profiles, marking the active one",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return renderProfiles(cmd.OutOrStdout(), app)
		},
	}
}

// renderProfiles lists the profiles with the active one marked. The marker is
// a "*" as well as a color, so the active profile is still identifiable in a
// pipe or with NO_COLOR set.
func renderProfiles(out io.Writer, app *config.AppConfig) error {
	r := newRenderer(out)
	router := app.Provider.Router
	fileNames, err := config.ListProfiles(config.InvokrHomeDir())
	if err != nil {
		return err
	}
	if !router.Enabled() && len(fileNames) == 0 {
		_, err := fmt.Fprintf(out, "%s %s\n", r.muted("no provider profiles configured; using"), app.Provider.BaseURL)
		return err
	}
	ids := router.ProfileIDs()
	active := router.Active
	if active == "" && len(ids) == 1 {
		active = ids[0]
	}
	rows := make([][]string, 0, len(ids)+len(fileNames))
	for _, id := range ids {
		profile := router.Profiles[id]
		marker, name := markerFor(r, id, id == active)
		rows = append(rows, []string{marker, name, profile.Model, r.muted(profile.Provider), ""})
	}
	rows = append(rows, fileProfileRows(r, fileNames, router, active)...)
	_, err = fmt.Fprintln(out, r.table([]string{"", "PROFILE", "MODEL", "PROVIDER", "SOURCE"}, rows))
	return err
}

// fileProfileRows lists the markdown profiles under ~/.invokr/profiles that
// the router table does not already show (a file profile that was applied this
// run appears there as its synthetic router entry).
func fileProfileRows(r renderer, names []string, router config.RouterConfig, active string) [][]string {
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		if _, shown := router.Profiles[name]; shown {
			continue
		}
		marker, label := markerFor(r, name, name == active)
		rows = append(rows, []string{marker, label, "", "", r.muted("file")})
	}
	return rows
}

// markerFor paints the active profile's row: a "*" as well as a color, so the
// active one is still identifiable in a pipe or with NO_COLOR set.
func markerFor(r renderer, id string, active bool) (string, string) {
	if !active {
		return " ", id
	}
	return r.accent("*"), r.accent(id)
}

// newUseProfileCmd persists a new active profile. It writes the config file
// rather than mutating the loaded copy, so the change outlives the process.
func newUseProfileCmd(app *config.AppConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "use <profile>",
		Aliases: []string{"switch"},
		Short:   "Switch the active provider profile",
		Long:    "Writes the choice to the config file, so it applies to later runs too. `invokr config profiles` lists the valid names.",
		Example: "  invokr config use anthropic",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.DefaultConfigPath()
			if err := config.SetActiveProfile(path, args[0]); err != nil {
				return err
			}
			app.Provider.Router.Active = args[0]
			profile := app.Provider.Router.Profiles[args[0]]
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "active profile is now %s (%s via %s)\n", args[0], profile.Model, profile.Provider)
			return nil
		},
	}
}
