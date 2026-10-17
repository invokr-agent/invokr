package memory

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"github.com/invokr-ai/invokr/capability/memory"
	memorycore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/tools/core"
	"strings"
)

// memoryToolArgs is flat (no nested record/filter object) so weaker models can
// call it reliably. Record fields (scope/kind/content/summary/source) feed
// save+update; filter fields (query/scopes/kinds/limit) feed list; session_id
// and id are shared.
type memoryToolArgs struct {
	Action    string               `json:"action" desc:"Memory action to perform." enum:"save,update,delete,list"`
	ID        string               `json:"id,omitempty" desc:"Memory record id. Required for update and delete."`
	Scope     config.MemoryScope   `json:"scope,omitempty" enum:"user,project,session"`
	Kind      memorycore.Kind      `json:"kind,omitempty" enum:"profile,preference,fact,decision,episode,summary"`
	Content   string               `json:"content,omitempty"`
	Summary   string               `json:"summary,omitempty"`
	Source    memorycore.Source    `json:"source,omitempty" desc:"Storage tier. Use markdown for durable, always-injected facts and preferences — they are pinned into USER.md/MEMORY.md and included in every system prompt. Omit (or use sqlite) for episodic notes that only need to surface through recall." enum:"markdown,sqlite,runtime"`
	SessionID string               `json:"session_id,omitempty"`
	Query     string               `json:"query,omitempty"`
	Scopes    []config.MemoryScope `json:"scopes,omitempty" enum:"user,project,session"`
	Kinds     []memorycore.Kind    `json:"kinds,omitempty" enum:"profile,preference,fact,decision,episode,summary"`
	Limit     int                  `json:"limit,omitempty" desc:"Maximum number of records." min:"1" max:"10"`
}

// recallArgs mirrors memorycore.Query for the model-facing schema; the
// capability package stays untagged.
type recallArgs struct {
	Query     string               `json:"query" desc:"Search query. Required."`
	SessionID string               `json:"session_id,omitempty"`
	Scopes    []config.MemoryScope `json:"scopes,omitempty" enum:"user,project,session"`
	Kinds     []memorycore.Kind    `json:"kinds,omitempty" enum:"profile,preference,fact,decision,episode,summary"`
	Limit     int                  `json:"limit,omitempty" desc:"Maximum number of records." min:"1" max:"10"`
}

func (a recallArgs) query() memorycore.Query {
	return memorycore.Query{Query: a.Query, SessionID: a.SessionID, Scopes: a.Scopes, Kinds: a.Kinds, Limit: a.Limit}
}

func (a memoryToolArgs) record() memorycore.Record {
	return memorycore.Record{
		Scope: a.Scope, Kind: a.Kind, Content: a.Content,
		Summary: a.Summary, Source: a.Source, SessionID: a.SessionID,
	}
}

func (a memoryToolArgs) filter() memorycore.Query {
	return memorycore.Query{Query: a.Query, SessionID: a.SessionID, Scopes: a.Scopes, Kinds: a.Kinds, Limit: a.Limit}
}

// RegisterMemoryTools registers memory/recall without an approval gate: this
// is deliberate — memory writes touch only the agent's own record store, not
// workspace files, and prompting on every save would drown the user
// (decision recorded from the PR #131 manual test run, test 8).
func RegisterMemoryTools(c core.NativeRegistrar, deps core.CallerDeps) {
	manager := deps.Memory
	if manager == nil {
		return
	}

	core.Register(c, core.Tool[memoryToolArgs]{
		Spec: core.ToolSpec{
			Name: "memory", Description: "Manage persistent memory records. Actions: save, update, delete, list. For save/update set scope (user, project, session), kind (profile, preference, fact, decision, episode, summary), content, and optionally summary/session_id. Set source to markdown for a durable fact or preference that must be present in every future prompt (it is pinned into USER.md/MEMORY.md); leave source unset for episodic notes that only need to be findable via recall. Use id for update/delete. For list, filter with query, scopes, kinds, limit. All fields are top-level (no nested objects). Do not save conclusions about tool availability or capability drawn from a single denied or failed call — an approval denial applies to that one request, not the environment.",
			Example:    `{"name":"memory","arguments":{"action":"save","scope":"project","kind":"fact","source":"markdown","content":"Project uses PostgreSQL for storage."}}`,
			Parameters: core.SchemaFor[memoryToolArgs](), Metadata: core.ReadWorkspaceMetadata("memory", core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true}),
		},
		Decode: decodeMemoryToolArgs,
		Execute: func(ctx context.Context, args memoryToolArgs) (string, error) {
			return handleMemoryTool(ctx, manager, args)
		},
	})

	core.Register(c, core.Tool[recallArgs]{
		Spec: core.ToolSpec{
			Name: "recall", Description: "Search memory: saved records (user and project scope) plus recent conversation turns, ranked together. Arguments: query, session_id, scopes, kinds, limit. Use memory action list to enumerate saved records without a search query.",
			Example:    `{"name":"recall","arguments":{"query":"database migrations","kinds":["episode"],"limit":3}}`,
			Parameters: core.SchemaFor[recallArgs](), Metadata: core.ReadWorkspaceMetadata("memory", core.ToolCapabilities{ReadsWorkspace: true}),
		},
		Execute: func(ctx context.Context, args recallArgs) (string, error) {
			return handleRecallTool(ctx, manager, args.query())
		},
	})

	registerArchiveTool(c, manager, deps.Approver)
}

func decodeMemoryToolArgs(call *core.ToolCall) (memoryToolArgs, error) {
	var args memoryToolArgs
	if call == nil {
		return args, fmt.Errorf("tool call is required")
	}
	if len(bytes.TrimSpace(call.Arguments)) == 0 {
		return args, fmt.Errorf("memory action is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, fmt.Errorf("parse memory arguments: %w", err)
	}
	if strings.TrimSpace(args.Action) == "" {
		return args, fmt.Errorf("memory action is required")
	}
	return args, nil
}

// recordResult is the single-record payload the save and update actions return.
type recordResult struct {
	Record memorycore.Record `json:"record"`
}

func handleMemoryTool(ctx context.Context, manager memory.Manager, args memoryToolArgs) (string, error) {
	action := strings.TrimSpace(strings.ToLower(args.Action))
	switch action {
	case "save":
		record := defaultMemoryRecord(args.record())
		saved, err := manager.Save(ctx, record)
		if err != nil {
			return "", err
		}
		return core.MarshalResult("memory", recordResult{Record: saved})
	case "update":
		id := strings.TrimSpace(args.ID)
		if id == "" {
			return "", fmt.Errorf("id is required")
		}
		// No defaults here: an update merges onto the stored record, so an
		// omitted field must stay empty to mean "unchanged" (issue #299).
		updated, err := manager.Update(ctx, id, args.record())
		if err != nil {
			return "", err
		}
		return core.MarshalResult("memory", recordResult{Record: updated})
	case "delete":
		id := strings.TrimSpace(args.ID)
		if id == "" {
			return "", fmt.Errorf("id is required")
		}
		if err := manager.Delete(ctx, id); err != nil {
			return "", err
		}
		return core.MarshalResult("memory", struct {
			Deleted string `json:"deleted"`
		}{Deleted: id})
	case "list":
		records, err := manager.List(ctx, args.filter())
		if err != nil {
			return "", err
		}
		return core.MarshalResult("memory", struct {
			Records []memorycore.Record `json:"records"`
		}{Records: records})
	default:
		return "", fmt.Errorf("unsupported memory action %q", args.Action)
	}
}

func handleRecallTool(ctx context.Context, manager memory.Manager, args memorycore.Query) (string, error) {
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	// Default to the current session so the raw-turn fallback can surface what
	// was just discussed when the model omits session_id.
	if strings.TrimSpace(args.SessionID) == "" && defaultsToCurrentSession(args.Scopes) {
		args.SessionID = core.SessionIDFromContext(ctx)
	}
	args.Limit = mathutil.ClampInt(args.Limit, 3, 10)

	hits, err := manager.Recall(ctx, args)
	if err != nil {
		return "", err
	}
	return core.MarshalResult("recall", struct {
		Hits []memorycore.Hit `json:"hits"`
	}{Hits: hits})
}

func defaultsToCurrentSession(scopes []config.MemoryScope) bool {
	return len(scopes) == 0 || len(scopes) == 1 && scopes[0] == config.ScopeSession
}

func defaultMemoryRecord(record memorycore.Record) memorycore.Record {
	record.Scope = cmp.Or(record.Scope, config.ScopeProject)
	record.Kind = cmp.Or(record.Kind, memorycore.KindFact)
	record.Source = cmp.Or(record.Source, memorycore.SourceRuntime)
	return record
}
