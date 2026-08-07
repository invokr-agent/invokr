package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/memory"
	memorycore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/tools/core"
)

// archiveToolArgs drives the recall_archive tool. A query searches the cold
// archive; restore brings the listed record IDs back to active memory. Exactly
// one mode is used per call.
type archiveToolArgs struct {
	Query   string   `json:"query,omitempty" desc:"Lexical search over forgotten memory."`
	Restore []string `json:"restore,omitempty" desc:"Record ids to restore to active memory."`
	Limit   int      `json:"limit,omitempty" desc:"Maximum records to return when searching." min:"1" max:"25"`
}

// registerArchiveTool registers recall_archive — the only reader of forgotten
// (archived) memory. Unlike memory/recall it carries an Approve fn, so every
// call surfaces an approval prompt: the user explicitly consents each time
// forgotten memory is searched or restored. Under an unattended deny policy the
// archive is unreachable.
func registerArchiveTool(c core.NativeRegistrar, manager memory.Manager, approver approval.Approver) {
	core.Register(c, core.Tool[archiveToolArgs]{
		Spec: core.ToolSpec{
			Name: "recall_archive", Description: "Search or restore forgotten memory. Archived records are memories that decayed out of active recall; no automatic path ever reads them. Use this only when the user explicitly asks to recover something old or forgotten. Set query to search the archive; set restore to a list of record ids (from a prior search) to bring them back to active memory. Every call requires user approval.",
			Example:    `{"name":"recall_archive","arguments":{"query":"database migration decision","limit":5}}`,
			Parameters: core.SchemaFor[archiveToolArgs](),
			Metadata:   core.NativeMetadata("memory", nil, constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true}),
		},
		Approve: func(_ context.Context, args archiveToolArgs) core.ApprovalDecision {
			return core.Ask(approval.Request{
				Tool: "recall_archive", Summary: archiveApprovalSummary(args),
				Reason: "recall_archive reads or restores memory that was deliberately forgotten",
			})
		},
		Approver: approver,
		Execute: func(ctx context.Context, args archiveToolArgs) (string, error) {
			return handleArchiveTool(ctx, manager, args)
		},
	})
}

func archiveApprovalSummary(args archiveToolArgs) string {
	if len(args.Restore) > 0 {
		return fmt.Sprintf("Restore %d forgotten memory record(s)", len(args.Restore))
	}
	return fmt.Sprintf("Search forgotten memory for %q", strings.TrimSpace(args.Query))
}

func handleArchiveTool(ctx context.Context, manager memory.Manager, args archiveToolArgs) (string, error) {
	if len(args.Restore) > 0 {
		restored, err := manager.RestoreArchived(ctx, args.Restore)
		if err != nil {
			return "", err
		}
		return core.MarshalResult("recall_archive", struct {
			Restored []string `json:"restored"`
		}{Restored: restored})
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("query or restore is required")
	}
	limit := mathutil.ClampInt(args.Limit, 10, 25)
	records, err := manager.SearchArchive(ctx, query, limit)
	if err != nil {
		return "", err
	}
	return core.MarshalResult("recall_archive", struct {
		Records []memorycore.Record `json:"records"`
	}{Records: records})
}
