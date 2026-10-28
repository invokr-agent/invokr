package session_search

import (
	"context"
	"fmt"
	"github.com/invokr-agent/invokr/capability/sessionstore"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/tools/core"
	"slices"
	"strings"
	"time"
)

const ToolName = "session_search"

const (
	actionSearch = "search"
	actionRecent = "recent"
	actionLoad   = "load"
)

const (
	defaultMaxResults = 5
	messageLimit      = 500
	transcriptBudget  = 8000
)

type Args struct {
	Action     string `json:"action,omitempty" desc:"search (default), recent, or load." enum:"search,recent,load"`
	Query      string `json:"query,omitempty" desc:"Lexical search query (required for action search)."`
	SessionID  string `json:"session_id,omitempty" desc:"Session to load (action load). Defaults to the most recent session."`
	MaxResults int    `json:"max_results,omitempty" desc:"Maximum results for search/recent."`
	// All widens search/recent past the current working directory.
	All bool `json:"all,omitempty" desc:"Search every directory's sessions. Defaults to false: only sessions from the current working directory."`
}

type searchResponse struct {
	Hits []sessionstore.SearchHit `json:"hits"`
}

type sessionSummary struct {
	SessionID string    `json:"session_id"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  int       `json:"messages"`
	Preview   string    `json:"preview"`
}

type recentResponse struct {
	Sessions []sessionSummary `json:"sessions"`
}

type transcriptMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type loadResponse struct {
	SessionID string              `json:"session_id"`
	Messages  []transcriptMessage `json:"messages"`
}

func RegisterSessionSearchTool(registrar core.NativeRegistrar, deps core.CallerDeps) {
	store := sessionstore.NewForWorkspace(deps.App.Memory.Dir, deps.App.Workspace.Root)
	core.Register(registrar, core.Tool[Args]{
		Spec: core.ToolSpec{
			Name: ToolName, Description: "Recall prior Invokr sessions from the current working directory (pass all=true for every directory). For \"what did we last talk about\", use action \"recent\" to list the latest sessions, then action \"load\" with a session_id to read that transcript (session_id defaults to the most recent). action \"search\" (default) is a lexical keyword lookup across sessions.",
			Example:    `{"name":"session_search","arguments":{"action":"recent","max_results":3}}`,
			Parameters: core.SchemaFor[Args](), Metadata: core.NativeMetadata("memory", []string{"read_session"}, constants.ApprovalPolicyAuto, core.ToolCapabilities{}),
		},
		Execute: func(ctx context.Context, args Args) (string, error) {
			switch strings.ToLower(strings.TrimSpace(args.Action)) {
			case "", actionSearch:
				return searchAction(ctx, store, args)
			case actionRecent:
				return recentAction(ctx, store, args)
			case actionLoad:
				return loadAction(ctx, store, args)
			default:
				return "", fmt.Errorf("unsupported action %q (want search, recent, or load)", args.Action)
			}
		},
	})
}

func searchAction(ctx context.Context, store *sessionstore.Store, args Args) (string, error) {
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("query is required for search")
	}
	search := store.SearchInCwd
	if args.All {
		search = store.Search
	}
	hits, err := search(ctx, args.Query, args.MaxResults)
	if err != nil {
		return "", err
	}
	return marshal(searchResponse{Hits: hits})
}

func recentAction(ctx context.Context, store *sessionstore.Store, args Args) (string, error) {
	sessions, err := listSessions(store, args.All)
	if err != nil {
		return "", err
	}
	current := core.SessionIDFromContext(ctx)
	limit := args.MaxResults
	if limit <= 0 {
		limit = defaultMaxResults
	}
	out := make([]sessionSummary, 0, limit)
	for _, s := range sessions {
		if s.ID == current {
			continue
		}
		out = append(out, sessionSummary{SessionID: s.ID, UpdatedAt: s.UpdatedAt, Messages: s.Messages, Preview: s.Preview})
		if len(out) >= limit {
			break
		}
	}
	return marshal(recentResponse{Sessions: out})
}

func loadAction(ctx context.Context, store *sessionstore.Store, args Args) (string, error) {
	sid := strings.TrimSpace(args.SessionID)
	if sid == "" {
		sid = mostRecentSessionID(store, core.SessionIDFromContext(ctx))
	}
	if sid == "" {
		return "", fmt.Errorf("no session to load")
	}
	messages, err := store.Load(ctx, sid)
	if err != nil {
		return "", err
	}
	return marshal(loadResponse{SessionID: sid, Messages: truncateTranscript(messages)})
}

func listSessions(store *sessionstore.Store, all bool) ([]sessionstore.SessionInfo, error) {
	sessions, err := store.List()
	if err != nil || all {
		return sessions, err
	}
	return sessionstore.FilterByCwd(sessions, store.Cwd()), nil
}

func mostRecentSessionID(store *sessionstore.Store, current string) string {
	sessions, err := listSessions(store, false)
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		if s.ID != current {
			return s.ID
		}
	}
	return ""
}

// truncateTranscript preserves newest messages within transcriptBudget, then
// restores chronological order for the model.
func truncateTranscript(messages []sessionstore.Message) []transcriptMessage {
	out := make([]transcriptMessage, 0, len(messages))
	total := 0
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		text := truncate(strings.TrimSpace(m.Text), messageLimit)
		if total+len(text) > transcriptBudget {
			break
		}
		total += len(text)
		out = append(out, transcriptMessage{Role: m.Role, Text: text})
	}
	slices.Reverse(out)
	return out
}

func truncate(s string, limit int) string {
	cut := core.TruncateRunes(s, limit)
	if cut == s {
		return s
	}
	return cut + "…"
}

func marshal(v any) (string, error) {
	return core.MarshalResult("session", v)
}
