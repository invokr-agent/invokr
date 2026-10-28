// Package retrieval is a thin search tool over the memory store (ADR 0015).
// Workspace docs (.md/.txt) are mirrored into memory_records with kind "doc";
// sqlite FTS covers the lexical half and the memory manager's vector tier the
// semantic half. The tool only (re)indexes stale docs and queries through the
// memory Manager — it keeps no private index, scorer, or embedding cache.
package retrieval

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/capability/memory"
	memorycore "github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/support/mathutil"
	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

const (
	ToolName                = "retrieval"
	ActionSearch            = "retrieval_search"
	ActionIndex             = "retrieval_index"
	ReadWorkspacePolicyTier = "read_workspace"
	IndexWritePolicyTier    = "retrieval_index_write"
	defaultMaxResults       = 8
	maxResultsCap           = 50
)

type Args struct {
	Query      string `json:"query" desc:"Search query."`
	Scope      string `json:"scope,omitempty" enum:"project_docs,memory,project_docs_and_memory" desc:"Search scope."`
	MaxResults int    `json:"max_results,omitempty" desc:"Maximum hits, capped at 50."`
}

type Response struct {
	Hits []Hit `json:"hits"`
}

type Hit struct {
	SourceType     string  `json:"source_type"`
	PathOrID       string  `json:"path_or_id"`
	Snippet        string  `json:"snippet"`
	Score          float64 `json:"score"`
	SourceHash     string  `json:"source_hash"`
	IndexedAt      string  `json:"indexed_at"`
	Stale          bool    `json:"stale"`
	LineStart      int     `json:"line_start"`
	LineEnd        int     `json:"line_end"`
	MemoryRecordID *string `json:"memory_record_id"`
}

type Service struct {
	app        config.AppConfig
	memory     memory.Manager
	dispatcher events.Dispatcher
	approver   approval.Approver
	workspace  workspace.Policy
	now        func() time.Time
}

func NewService(app config.AppConfig, manager memory.Manager, dispatcher events.Dispatcher, wsp workspace.Policy) *Service {
	return &Service{
		app: app.WithDefaults(), memory: manager, dispatcher: dispatcher, approver: nil,
		workspace: wsp, now: func() time.Time { return time.Now().UTC() },
	}
}

func NewServiceWithApprover(app config.AppConfig, manager memory.Manager, dispatcher events.Dispatcher, approver approval.Approver, wsp workspace.Policy) *Service {
	s := NewService(app, manager, dispatcher, wsp)
	s.approver = approver
	return s
}

func RegisterRetrievalTool(c core.NativeRegistrar, deps core.CallerDeps, wsp workspace.Policy) {
	service := NewServiceWithApprover(deps.App, deps.Memory, deps.Dispatcher, deps.Approver, wsp)
	core.Register(c, core.Tool[Args]{
		Spec: core.ToolSpec{
			Name: ToolName, Description: "Search indexed project docs and memory records. Uses read_workspace and retrieval_index_write policy tiers.",
			Example:    `{"name":"retrieval","arguments":{"query":"toolbox plugin loading","scope":"project_docs_and_memory","max_results":8}}`,
			Parameters: core.SchemaFor[Args](), Metadata: core.NativeMetadata("workspace", []string{ReadWorkspacePolicyTier, IndexWritePolicyTier}, constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true}),
		},
		Prepare: func(ctx context.Context, args Args) (Args, error) {
			args = normalizeArgs(args)
			if args.Query == "" {
				return args, fmt.Errorf("query is required")
			}
			return args, nil
		},
		Execute: func(ctx context.Context, args Args) (string, error) { return service.RunToolArgs(ctx, args) },
	})
}

func (s *Service) RunToolArgs(ctx context.Context, args Args) (string, error) {
	started := s.now()
	docHits, sourceCount, err := s.searchDocs(ctx, args)
	var memHits []Hit
	if err == nil {
		memHits, err = s.memoryHits(ctx, args)
	}
	if err != nil {
		s.dispatchAudit(ctx, ActionSearch, args, sourceCount, 0, 0, s.now().Sub(started), "failed", err)
		return "", err
	}
	hits := mergeHits(docHits, memHits, args.MaxResults)
	staleCount := 0
	for _, hit := range hits {
		if hit.Stale {
			staleCount++
		}
	}
	s.dispatchAudit(ctx, ActionSearch, args, sourceCount, len(hits), staleCount, s.now().Sub(started), "completed", nil)

	return core.MarshalResult("retrieval", Response{Hits: hits})
}

// searchDocs syncs workspace docs into the memory store (first sync gated on
// approval) and recalls doc-kind records matching the query.
func (s *Service) searchDocs(ctx context.Context, args Args) ([]Hit, int, error) {
	if s.memory == nil || args.Scope == "memory" {
		return nil, 0, nil
	}
	started := s.now()
	sync, err := s.syncDocs(ctx, syncOptions{approveInitial: true})
	if sync.changed || err != nil {
		s.dispatchAudit(ctx, ActionIndex, Args{}, sync.sourceCount, 0, len(sync.stale), s.now().Sub(started), core.Outcome(err), err)
	}
	if err != nil {
		return nil, sync.sourceCount, err
	}
	hits, err := s.docHits(ctx, args, sync.stale)
	return hits, sync.sourceCount, err
}

func (s *Service) docHits(ctx context.Context, args Args, stale map[string]bool) ([]Hit, error) {
	// Workspace-scoped: memory.db is user-global, so an unscoped recall would
	// return another repository's docs for this workspace's query.
	query := s.docQuery()
	query.Query = args.Query
	query.Limit = args.MaxResults
	recalled, err := s.memory.Recall(ctx, query)
	if err != nil {
		return nil, err
	}
	queryTokens := core.NormalizeStrings(tokenize(args.Query), false)
	hits := make([]Hit, 0, len(recalled))
	for _, hit := range recalled {
		path := hit.Record.Metadata[docMetaPath]
		if path == "" {
			continue
		}
		hits = append(hits, docHitFromRecall(hit, path, queryTokens, stale[path]))
	}
	return hits, nil
}

func docHitFromRecall(hit memorycore.Hit, path string, queryTokens []string, stale bool) Hit {
	id := hit.Record.ID
	lineEnd, _ := strconv.Atoi(hit.Record.Metadata[docMetaLines])
	if lineEnd <= 0 {
		lineEnd = countLines([]byte(hit.Record.Content))
	}
	return Hit{
		SourceType: "file", PathOrID: path, Snippet: snippetFor(hit.Record.Content, queryTokens),
		Score: mathutil.Clamp01(hit.Score), SourceHash: hit.Record.Metadata[docMetaHash],
		IndexedAt: recordTimestamp(hit.Record), Stale: stale, LineStart: 1, LineEnd: lineEnd, MemoryRecordID: &id,
	}
}

// memoryHits recalls memory records through the memory Manager when the scope
// includes memory. Doc-kind records are excluded by the store's default kind
// filter, so they never double-report as memory hits.
func (s *Service) memoryHits(ctx context.Context, args Args) ([]Hit, error) {
	if s.memory == nil || (args.Scope != "memory" && args.Scope != "project_docs_and_memory") {
		return nil, nil
	}
	recalled, err := s.memory.Recall(ctx, memorycore.Query{Query: args.Query, Limit: args.MaxResults})
	if err != nil {
		return nil, err
	}
	queryTokens := core.NormalizeStrings(tokenize(args.Query), false)
	return core.MapSlice(recalled, func(hit memorycore.Hit) Hit {
		return s.memoryHitFromRecall(hit, queryTokens)
	}), nil
}

func (s *Service) memoryHitFromRecall(hit memorycore.Hit, queryTokens []string) Hit {
	content := strings.TrimSpace(strutil.FirstNonEmpty(hit.Record.Summary, hit.Record.Content))
	id := hit.Record.ID
	return Hit{
		SourceType: "memory", PathOrID: id, Snippet: snippetFor(content, queryTokens),
		Score: mathutil.Clamp01(hit.Score), SourceHash: "sha256:" + strutil.HashHex(content),
		IndexedAt: recordTimestamp(hit.Record), Stale: false, LineStart: 1,
		LineEnd: countLines([]byte(content)), MemoryRecordID: &id,
	}
}

func recordTimestamp(rec memorycore.Record) string {
	if !rec.UpdatedAt.IsZero() {
		return rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !rec.CreatedAt.IsZero() {
		return rec.CreatedAt.UTC().Format(time.RFC3339)
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// mergeHits combines file and memory hits under the shared ordering (score
// desc, then source type, then path) and truncates to maxResults.
func mergeHits(fileHits, memHits []Hit, maxResults int) []Hit {
	hits := make([]Hit, 0, len(fileHits)+len(memHits))
	hits = append(hits, fileHits...)
	hits = append(hits, memHits...)
	slices.SortFunc(hits, func(a, b Hit) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.SourceType, b.SourceType), cmp.Compare(a.PathOrID, b.PathOrID))
	})
	return hits[:min(len(hits), maxResults)]
}

// dispatchAudit emits one retrieval audit event with the shared fields folded in.
func (s *Service) dispatchAudit(ctx context.Context, action string, args Args, sourceCount int, hitCount int, staleCount int, duration time.Duration, outcome string, err error) {
	details := map[string]any{
		"source_count": sourceCount, "hit_count": hitCount, "stale_count": staleCount,
		"index_path":   filepath.ToSlash(s.storePath()),
		"policy_tiers": []string{ReadWorkspacePolicyTier, IndexWritePolicyTier},
		"duration_ms":  duration.Milliseconds(),
	}
	if args.Scope != "" {
		details["scope"] = args.Scope
	}
	if args.Query != "" {
		details["query_hash"] = strutil.HashHexTrimmed(args.Query)
	}
	core.DispatchAudit(ctx, s.dispatcher, &events.AuditPayload{Tool: ToolName, Action: action, Outcome: outcome, Details: details}, err)
}

func normalizeArgs(args Args) Args {
	args.Query = strings.TrimSpace(args.Query)
	args.Scope = strings.TrimSpace(args.Scope)
	if args.Scope != "project_docs" && args.Scope != "memory" {
		args.Scope = "project_docs_and_memory"
	}
	args.MaxResults = mathutil.ClampInt(args.MaxResults, defaultMaxResults, maxResultsCap)
	return args
}
