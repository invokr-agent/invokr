package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Command groups shown in `invokr --help`. A flat list of a dozen top-level
// commands hides the two people reach for first, so help is grouped by intent
// rather than alphabetically.
const (
	groupRun       = "run"
	groupInspect   = "inspect"
	groupConfigure = "configure"
	groupServe     = "serve"
)

func rootCommandGroups() []*cobra.Group {
	return []*cobra.Group{
		{ID: groupRun, Title: "Run the agent:"}, {ID: groupInspect, Title: "Inspect what ran:"},
		{ID: groupConfigure, Title: "Configure and diagnose:"}, {ID: groupServe, Title: "Long-running surfaces:"},
	}
}

// useDefaultSubcommand makes a bare parent invocation run one of its children
// instead of printing help. `invokr sessions` should show the session history,
// the same way `invokr` alone starts the agent; a group command that only
// prints its own help costs a round trip to learn a name the user already
// implied.
//
// The child's flags are mirrored onto the parent so `invokr sessions --json`
// binds the same variables the child's RunE closes over. Only ever called at
// registration time, so a missing child is a programming error, not a runtime
// one.
func useDefaultSubcommand(parent *cobra.Command, name string) {
	child, _, err := parent.Find([]string{name})
	if err != nil || child == parent {
		panic("cmd: default subcommand " + name + " is not registered on " + parent.Name())
	}
	parent.RunE = func(cmd *cobra.Command, args []string) error { return child.RunE(cmd, args) }
	parent.Args = cobra.NoArgs
	parent.Flags().AddFlagSet(child.Flags())
}

// retiredCommand fails loudly instead of falling through to a parent's help
// or a lookalike command, so a script using the old spelling gets a clear
// "retired, use X" error (docs/private/reviews/2026-08-19-cli-surface-review.md).
func retiredCommand(use, replacement string) *cobra.Command {
	return &cobra.Command{
		Use:    use,
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("%s is retired; use %s", cmd.CommandPath(), replacement)
		},
	}
}
