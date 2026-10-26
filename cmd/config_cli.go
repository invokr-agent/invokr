package cmd

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"

	"github.com/invokr-ai/invokr/config"

	"github.com/spf13/cobra"
)

func NewConfigCmd(app *config.AppConfig) *cobra.Command {
	configCmd := &cobra.Command{
		Use:     "config",
		Short:   "Show effective configuration and switch profiles",
		Long:    "The effective config is what Invokr actually runs with: the file at\n~/.invokr/config.json, overlaid with INVOKR_* environment variables\nand the flags on this invocation. Run bare, it prints a summary of the\nsettings people look for; --json prints all of it, secrets redacted.\n\nA profile can set provider, model, prompt, tools, and mode. `profiles` lists\nthem and `use` switches which is active, writing the choice back to the config\nfile so it outlives the process.",
		Example: "  invokr config                  # summary: provider, model, mode, paths\n  invokr config --json           # the full effective config, secrets redacted\n  invokr config profiles         # configured profiles, active one marked *\n  invokr config use anthropic",
		GroupID: groupConfigure,
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Summarise the effective configuration; --json for all of it",
		Long:  "File, then INVOKR_* environment overrides, then this invocation's flags.\nBare, a summary; --json, the whole thing with secrets redacted.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON, _ := cmd.Flags().GetBool("json"); !asJSON {
				return renderConfigSummary(cmd.OutOrStdout(), app)
			}
			v, err := readableDurations(app.Redacted())
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(v)
		},
	}
	show.Flags().Bool("json", false, "Print the full effective config as JSON, secrets redacted")
	configCmd.AddCommand(show)
	useDefaultSubcommand(configCmd, "show", newProfilesCmd(app), newUseProfileCmd(app))
	return configCmd
}

// renderConfigSummary is the bare `invokr config`: the handful of settings
// people look for, one per line, instead of the whole document.
func renderConfigSummary(out io.Writer, app *config.AppConfig) error {
	home := config.InvokrHomeDir()
	rows := [][2]string{
		{"provider", app.Provider.BaseURL},
		{"model", cmp.Or(app.Provider.Model, "(first model the server lists)")},
		{"profile", cmp.Or(app.Profile, "(none)")},
		{"mode", cmp.Or(app.SessionMode, "manual")},
		{"home", home},
		{"config", filepath.Join(home, "config.json")},
		{"skills", app.Skills.Dir},
		{"agents", app.Agents.Dir},
		{"toolbox", app.Toolbox.Dir},
		{"log level", app.Log.Level},
	}
	r := newRenderer(out)
	for _, row := range rows {
		if _, err := fmt.Fprintf(out, "%-10s %s\n", r.muted(row[0]), row[1]); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, r.muted("\ninvokr config --json for every setting"))
	return err
}

// readableDurations swaps the config's time.Duration fields (raw nanoseconds
// in JSON) for their String form, e.g. "10m0s", for display only.
func readableDurations(c config.AppConfig) (map[string]any, error) {
	data, err := json.Marshal(c)
	var m map[string]any
	if err == nil {
		err = json.Unmarshal(data, &m)
	}
	for k, d := range map[[2]string]time.Duration{{"provider", "http_timeout"}: c.Provider.HTTPTimeout, {"provider", "stall_timeout"}: c.Provider.StallTimeout, {"agent", "run_timeout"}: c.Agent.RunTimeout} {
		if section, ok := m[k[0]].(map[string]any); ok && d != 0 {
			section[k[1]] = d.String()
		}
	}
	return m, err
}

// newProfilesCmd lists the configured provider profiles, marking the active
// one, so a user can see what "config use" accepts.
func newProfilesCmd(app *config.AppConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profiles",
		Short: "List profiles and mark the active one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := config.ListProfiles(config.InvokrHomeDir())
			if err != nil {
				return err
			}
			router := app.Provider.Router
			v := map[string]any{"active": cmp.Or(app.Profile, router.Active), "router": router.Profiles, "files": files}
			return emit(cmd, v, func() error { return renderProfiles(cmd.OutOrStdout(), app, files) })
		},
	}
	addJSONFlag(cmd)
	return cmd
}

// renderProfiles lists the profiles with the active one marked. The marker is
// a "*" as well as a color, so the active profile is still identifiable in a
// pipe or with NO_COLOR set.
func renderProfiles(out io.Writer, app *config.AppConfig, fileNames []string) error {
	r := newRenderer(out)
	router := app.Provider.Router
	if !router.Enabled() && len(fileNames) == 0 {
		_, err := fmt.Fprintf(out, "%s %s\n", r.muted("no provider profiles configured; using"), app.Provider.BaseURL)
		return err
	}
	ids := router.ProfileIDs()
	// A persisted file profile outranks router.active, as it does at load.
	active := cmp.Or(app.Profile, router.Active)
	if active == "" && len(ids) == 1 {
		active = ids[0]
	}
	rows := make([][]string, 0, len(ids)+len(fileNames))
	for _, id := range ids {
		if slices.Contains(fileNames, id) {
			continue // the file profile of that name wins; it is listed below
		}
		profile := router.Profiles[id]
		marker, name := markerFor(r, id, id == active)
		rows = append(rows, []string{marker, name, profile.Model, r.muted(profile.Provider), ""})
	}
	rows = append(rows, fileProfileRows(r, fileNames, active)...)
	_, err := fmt.Fprint(out, r.table([]string{"", "PROFILE", "MODEL", "PROVIDER", "SOURCE"}, rows))
	return err
}

// fileProfileRows lists profiles under ~/.invokr/profiles; a router profile
// of the same name is shadowed by them.
func fileProfileRows(r renderer, names []string, active string) [][]string {
	rows := make([][]string, 0, len(names))
	for _, name := range names {
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
		Short:   "Switch the active profile (model, prompt, tools, and mode)",
		Long:    "Writes the choice to the config file, so it applies to later runs too. `invokr config profiles` lists the valid names.",
		Example: "  invokr config use anthropic",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.DefaultConfigPath()
			if err := config.SetActiveProfile(path, args[0]); err != nil {
				return err
			}
			file, err := config.IsFileProfile(config.InvokrHomeDir(), args[0])
			if err != nil {
				return err
			}
			profile, ok := app.Provider.Router.Profiles[args[0]]
			if file || !ok {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "active profile is now %s\n", args[0])
				return err
			}
			app.Provider.Router.Active = args[0]
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "active profile is now %s (%s via %s)\n", args[0], profile.Model, profile.Provider)
			return err
		},
	}
}
