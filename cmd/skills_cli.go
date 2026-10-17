package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/capability/skills"
	"github.com/invokr-ai/invokr/capability/skills/hub"
	"github.com/invokr-ai/invokr/config"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// NewSkillsCmd builds the `skills` command tree. Skills subcommands skip the
// root runtime bootstrap (see skipsRuntimeBootstrap): they only touch the
// filesystem, git, and plain HTTP.
func NewSkillsCmd(app *config.AppConfig) *cobra.Command {
	root := &cobra.Command{
		Use:     "skills",
		Short:   "List, search, install, and remove agent skills",
		Long:    "Skills are SKILL.md instruction packs the agent can load on demand.\nThey install from skill hubs (git repositories of skills), local\ndirectories, or raw https:// URLs to a SKILL.md.\n\nRun bare, this lists what is installed.",
		Example: "  invokr skills\n  invokr skills search review\n  invokr skills install code-review\n  invokr skills install --all\n  invokr skills rm code-review",
		GroupID: groupConfigure,
	}
	useDefaultSubcommand(root, "list", newSkillsListCmd(app), newSkillsSearchCmd(app), newSkillsInstallCmd(app), newSkillsRemoveCmd(app))
	return root
}

func newSkillsListCmd(app *config.AppConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed skills",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			specs, err := skills.NewManagerFromConfig(*app, zap.NewNop()).Specs(cmd.Context())
			if err != nil {
				return err
			}
			return emit(cmd, specs, func() error {
				return renderTable(cmd.OutOrStdout(), []string{"NAME", "DESCRIPTION"},
					"no skills installed; see `invokr skills install --help`", specs,
					func(_ renderer, s skills.Spec) []string { return []string{s.Name, s.Description} })
			})
		},
	}
	addJSONFlag(cmd)
	return cmd
}

func newSkillsSearchCmd(app *config.AppConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search [term]",
		Short: "Search the configured skill hubs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			term := ""
			if len(args) == 1 {
				term = strings.ToLower(args[0])
			}
			svc := hub.NewService(*app)
			found, errs := svc.Skills(cmd.Context(), true)
			for _, err := range errs {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", err)
			}
			return renderSkillsSearch(cmd, svc, matchSkills(found, term))
		},
	}
	addJSONFlag(cmd)
	return cmd
}

func matchSkills(found []hub.HubSkill, term string) []hub.HubSkill {
	return slices.DeleteFunc(found, func(hs hub.HubSkill) bool {
		return term != "" && !strings.Contains(strings.ToLower(hs.Name), term) && !strings.Contains(strings.ToLower(hs.Description), term)
	})
}

func renderSkillsSearch(cmd *cobra.Command, svc *hub.Service, matched []hub.HubSkill) error {
	return emit(cmd, matched, func() error {
		return renderTable(cmd.OutOrStdout(), []string{"NAME", "HUB", "DESCRIPTION"}, "no matching skills in hubs "+strings.Join(svc.Hubs(), ", "), matched,
			func(_ renderer, hs hub.HubSkill) []string { return []string{hs.Name, hs.Hub, hs.Description} })
	})
}

func newSkillsInstallCmd(app *config.AppConfig) *cobra.Command {
	var name string
	var all bool
	cmd := &cobra.Command{
		Use:     "install [<name>|<dir>|<url>]",
		Short:   "Install a skill from a hub, local directory, or SKILL.md URL",
		Example: "  invokr skills install code-review\n  invokr skills install ./my-skill\n  invokr skills install https://example.com/SKILL.md --name my-skill\n  invokr skills install --all",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := hub.NewService(*app)
			out := cmd.OutOrStdout()
			if all {
				if len(args) != 0 {
					return fmt.Errorf("--all takes no arguments")
				}
				names, errs := svc.InstallAll(cmd.Context())
				for _, err := range errs {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", err)
				}
				if len(names) == 0 {
					return fmt.Errorf("no skills installed")
				}
				_, err := fmt.Fprintf(out, "installed %d skills: %s\n", len(names), strings.Join(names, ", "))
				return err
			}
			if len(args) != 1 {
				return fmt.Errorf("a skill name, directory, or URL is required (or --all)")
			}
			installed, err := svc.Install(cmd.Context(), args[0], name)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "skill %s installed\n", installed)
			return err
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Skill name to install as (required for URL installs)")
	cmd.Flags().BoolVar(&all, "all", false, "Install every skill from the configured hubs")
	return cmd
}

func newSkillsRemoveCmd(app *config.AppConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove an installed skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := hub.NewService(*app).Remove(args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "skill %s removed\n", args[0])
			return err
		},
	}
}
