package cmd

import (
	"fmt"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/tools/system/retrieval"
	"github.com/invokr-ai/invokr/tools/system/workspace"

	"github.com/spf13/cobra"
)

func NewIndexCmd(services func() (runtime.Services, error)) *cobra.Command {
	var allowLarge bool
	cmd := &cobra.Command{
		Use:     "index",
		Aliases: []string{"reindex"},
		Short:   "Build the retrieval index over the workspace",
		Long:    "The retrieval tool searches workspace docs mirrored into the memory\nstore as doc records. Building them up front makes the first search in\na session fast; without it the workspace is indexed lazily, and very\nlarge workspaces are skipped unless --allow-large says otherwise.\n\nRe-running it re-syncs changed docs and prunes records whose files\nare gone.",
		Example: "  invokr index\n  invokr index --allow-large",
		Args:    cobra.NoArgs,
		GroupID: groupInspect,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := services()
			if err != nil {
				return err
			}
			retrievalSvc := retrieval.NewService(svc.App, svc.Memory, svc.Events, workspace.NewWorkspacePolicy(svc.App.Workspace))
			result, err := retrievalSvc.BuildIndex(cmd.Context(), retrieval.BuildOptions{AllowLarge: allowLarge})
			if err != nil {
				return err
			}
			// Index build is the primary side effect; don't fail the command if
			// writing the success message fails (e.g. a closed stdout pipe).
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "indexed %d sources at %s\n", result.SourceCount, result.IndexPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowLarge, "allow-large", false, "Index the workspace even if it is above the size limit that normally skips it")
	return cmd
}
