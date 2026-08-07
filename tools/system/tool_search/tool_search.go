package tool_search

import (
	"cmp"
	"context"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/tools/core"
	"slices"
	"strings"
	"time"
)

const (
	defaultMaxResults = 10
	maxResultsCap     = 100
	policyTier        = "read_workspace"
	// A search narrow enough to return a handful of tools is a search for
	// something to call, so load those schemas without being asked: the
	// alternative is a hit that changes nothing and says nothing (#534).
	autoLoadMaxResults = 3
)

type searchArgs struct {
	Query         string `json:"query,omitempty" desc:"Optional text search over tool name, namespace, and description."`
	Namespace     string `json:"namespace,omitempty" desc:"Optional namespace filter."`
	MaxResults    int    `json:"max_results,omitempty" desc:"Maximum results, capped at 100."`
	IncludeSchema bool   `json:"include_schema,omitempty" desc:"Include parameters and mark returned tools loaded for this session."`
}

type searchResponse struct {
	Tools []toolResult `json:"tools"`
	Note  string       `json:"note,omitempty"`
}

type toolResult struct {
	Name         string                `json:"name"`
	Namespace    string                `json:"namespace"`
	Description  string                `json:"description"`
	Parameters   map[string]any        `json:"parameters,omitempty"`
	Source       string                `json:"source"`
	Runtime      string                `json:"runtime,omitempty"`
	PluginDir    string                `json:"plugin_dir,omitempty"`
	Policy       core.ToolPolicy       `json:"policy"`
	Capabilities core.ToolCapabilities `json:"capabilities"`
}

func RegisterToolSearchTool(c core.SearchableRegistry, deps core.CallerDeps) {
	core.Register(c, core.Tool[searchArgs]{
		Spec: core.ToolSpec{
			Name: "tool_search", Description: "Search the tool registry by name, namespace, or description. Uses read_workspace policy. Set include_schema=true before calling a deferred tool.",
			Example:    `{"name":"tool_search","arguments":{"query":"web","namespace":"network","max_results":10,"include_schema":true}}`,
			Parameters: core.SchemaFor[searchArgs](), Metadata: core.ReadWorkspaceMetadata("system", core.ToolCapabilities{ReadsWorkspace: true}),
		},
		Prepare: func(ctx context.Context, args searchArgs) (searchArgs, error) { return normalizeArgs(args), nil },
		AuditFailure: func(ctx context.Context, args searchArgs, err error) {
			dispatchAudit(deps.Dispatcher, ctx, args, 0, 0, "failed", err)
		},
		Execute: func(ctx context.Context, args searchArgs) (string, error) {
			return runToolSearch(deps.Dispatcher, ctx, c, args)
		},
	})
}

func runToolSearch(dispatcher events.Dispatcher, ctx context.Context, registry core.SearchableRegistry, args searchArgs) (payload string, err error) {
	started := time.Now()
	resultCount := 0
	defer func() {
		dispatchAudit(dispatcher, ctx, args, resultCount, time.Since(started), core.Outcome(err), err)
	}()

	if dispatcher != nil {
		dispatcher.Dispatch(ctx, events.Event{Type: events.EventToolboxRefreshRequested, SessionID: core.SessionIDFromContext(ctx)})
	}
	results := searchSpecs(registry.Specs(), args)
	resultCount = len(results)
	loaded := len(results) > 0 && (args.IncludeSchema || len(results) <= autoLoadMaxResults)
	if loaded && !args.IncludeSchema {
		// Narrow enough to be a search for something to call: re-run it as if
		// include_schema had been set, so the result carries the schema too.
		// args itself stays untouched — the audit records what was asked for.
		withSchema := args
		withSchema.IncludeSchema = true
		results = searchSpecs(registry.Specs(), withSchema)
	}
	if loaded {
		names := core.MapSlice(results, func(result toolResult) string { return result.Name })
		registry.MarkSchemasLoaded(core.SessionIDFromContext(ctx), names)
	}

	return core.MarshalResult("tool_search", searchResponse{Tools: results, Note: resultNote(loaded, len(results))})
}

// resultNote tells the model whether the tools it just found are callable, so
// a search without include_schema is never a silent no-op.
func resultNote(loaded bool, count int) string {
	if count == 0 {
		return ""
	}
	if loaded {
		return "schema loaded; call these tools directly now."
	}
	return "schemas not loaded; narrow the query or re-run with include_schema:true before calling these tools (a first direct call also loads the schema)."
}

func normalizeArgs(args searchArgs) searchArgs {
	args.Query = strings.TrimSpace(args.Query)
	args.Namespace = strings.TrimSpace(args.Namespace)
	args.MaxResults = mathutil.ClampInt(args.MaxResults, defaultMaxResults, maxResultsCap)
	return args
}

func searchSpecs(specs []core.ToolSpec, args searchArgs) []toolResult {
	slices.SortStableFunc(specs, func(a, b core.ToolSpec) int {
		return cmp.Or(cmp.Compare(a.Metadata.Namespace, b.Metadata.Namespace), cmp.Compare(a.Name, b.Name))
	})
	namespace := strings.ToLower(args.Namespace)
	results := make([]toolResult, 0, len(specs))
	for _, spec := range specs {
		meta := spec.Metadata
		lowerNamespace := strings.ToLower(meta.Namespace)
		if namespace != "" && lowerNamespace != namespace {
			continue
		}
		if !matchesQuery(spec, lowerNamespace, args.Query) {
			continue
		}
		result := toolResult{
			Name: spec.Name, Namespace: meta.Namespace, Description: spec.Description, Source: meta.Source,
			Runtime: meta.Runtime, PluginDir: meta.PluginDir, Policy: meta.Policy, Capabilities: meta.Capabilities,
		}
		if args.IncludeSchema {
			result.Parameters = spec.Parameters
		}
		results = append(results, result)
		if len(results) >= args.MaxResults {
			break
		}
	}
	return results
}

func matchesQuery(spec core.ToolSpec, namespace, query string) bool {
	haystack := strings.ToLower(spec.Name + " " + namespace + " " + spec.Description)
	for term := range strings.FieldsSeq(strings.ToLower(query)) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func dispatchAudit(dispatcher events.Dispatcher, ctx context.Context, args searchArgs, resultCount int, duration time.Duration, outcome string, err error) {
	core.AuditDetails(ctx, dispatcher, "tool_search", outcome, map[string]any{
		"policy_tier": policyTier, "query_hash": core.HashHexTrimmed(args.Query),
		"namespace": args.Namespace, "include_schema": args.IncludeSchema,
		"result_count": resultCount, "duration_ms": duration.Milliseconds(),
	}, err)
}
