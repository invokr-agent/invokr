package cmd

import (
	"fmt"
	"strings"

	"github.com/invokr-ai/invokr/internal/installer"
	"github.com/invokr-ai/invokr/platform/version"

	"github.com/spf13/cobra"
)

// runInstaller is a package var because installer.Run touches a real network and
// a real binary: tests drive the command through this instead of downloading a
// release over the one they are running.
var runInstaller = installer.Run

// NewUpdateCmd replaces the running binary with a release build.
//
// The install goes through the same script the README pipes to sh, embedded
// here at build time, so an update cannot drift from a fresh install: same
// download, same checksum verification, same placement. Invokr keeps nothing
// running in the background, so unlike a daemon there is no service to stop
// before the binary is replaced or to start after it; the next invocation is
// already the new build.
func NewUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update [version]",
		Short: "Install the latest release over this binary",
		Long: "Downloads a release, verifies its checksum, and replaces the binary this\n" +
			"command came from. Pass a version (e.g. 0.2.1 or v0.2.1) to install that release\n" +
			"instead of the newest.",
		Example: "  invokr update\n  invokr update 0.2.1",
		Args:    cobra.MaximumNArgs(1),
		GroupID: groupConfigure,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A dev build has no release it came from; silently replacing a
			// locally built binary with a release would be a nasty surprise.
			if version.Version == "dev" {
				return fmt.Errorf("this is a development build, not an installed release; build from source instead")
			}
			wanted := ""
			if len(args) == 1 {
				// Release tags carry a leading v; accept the bare number too.
				wanted = "v" + strings.TrimPrefix(args[0], "v")
			}
			return runUpdate(cmd, wanted)
		},
	}
}

func runUpdate(cmd *cobra.Command, wanted string) error {
	return runInstaller(cmd.Context(), wanted, cmd.OutOrStdout())
}
