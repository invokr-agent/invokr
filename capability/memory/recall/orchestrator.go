package recall

import (
	"context"
	"github.com/invokr-ai/invokr/support/strutil"
	"strings"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/policy"
)

type PinnedSource interface {
	PromptBlocks(ctx context.Context, req core.PromptRequest) (core.PromptBlocks, error)
	Records(ctx context.Context, filter core.Query) ([]core.Record, error)
}

type LexicalSource interface {
	Recall(ctx context.Context, req core.Query) ([]core.Hit, error)
}

type Orchestrator struct {
	pinned  PinnedSource
	lexical LexicalSource
	policy  policy.Policy
}

func NewOrchestrator(pinned PinnedSource, lexical LexicalSource, pol policy.Policy) *Orchestrator {
	return &Orchestrator{pinned: pinned, lexical: lexical, policy: pol}
}

func (o *Orchestrator) Policy() policy.Policy {
	if o == nil {
		return policy.Default
	}
	return o.policy
}

func (o *Orchestrator) PromptBlocks(ctx context.Context, req core.PromptRequest) (core.PromptBlocks, error) {
	if o == nil {
		return nil, nil
	}
	var blocks core.PromptBlocks
	if o.pinned != nil {
		var err error
		blocks, err = o.pinned.PromptBlocks(ctx, req)
		if err != nil {
			return nil, err
		}
	}
	block, err := o.tier2Block(ctx, req)
	if err != nil {
		return nil, err
	}
	if block.Content != "" {
		blocks = append(blocks, block)
	}
	// ApplyBudget sorts by Priority before truncating, so block order here
	// doesn't matter — the highest-priority blocks survive budget pressure.
	return blocks.ApplyBudget(req.BudgetChars), nil
}

func (o *Orchestrator) tier2Block(ctx context.Context, req core.PromptRequest) (core.PromptBlock, error) {
	if o.lexical == nil || !o.policy.Tier2.Enabled || !o.policy.Tier2.PromptEnabled {
		return core.PromptBlock{}, nil
	}
	query := strings.TrimSpace(req.UserMessage)
	if query == "" {
		return core.PromptBlock{}, nil
	}
	hits, err := o.lexical.Recall(ctx, core.Query{Query: query, SessionID: req.SessionID, Limit: o.policy.Tier2.Limit})
	if err != nil {
		return core.PromptBlock{}, err
	}
	if len(hits) == 0 {
		return core.PromptBlock{}, nil
	}
	return HitsPromptBlock(hits, 0), nil
}

func (o *Orchestrator) Recall(ctx context.Context, req core.Query) ([]core.Hit, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, nil
	}

	var hits []core.Hit
	if o != nil && o.lexical != nil {
		var err error
		if hits, err = o.lexical.Recall(ctx, req); err != nil {
			return nil, err
		}
	}
	if o == nil {
		return hits, nil
	}
	return o.appendPinned(ctx, req, hits)
}

func (o *Orchestrator) appendPinned(ctx context.Context, req core.Query, hits []core.Hit) ([]core.Hit, error) {
	if o.pinned == nil {
		return hits, nil
	}
	// A non-positive request limit means "no limit"; -1 passes that through.
	remaining := -1
	if req.Limit > 0 {
		if remaining = max(req.Limit-len(hits), 0); remaining == 0 {
			return hits, nil
		}
	}
	records, err := o.pinned.Records(ctx, core.Query{
		Query: req.Query, SessionID: req.SessionID, Scopes: req.Scopes, Kinds: req.Kinds, Limit: remaining})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		seen[RecordKey(hit.Record)] = struct{}{}
	}
	for _, rec := range records {
		hits = AppendUniqueHit(req.Limit, hits, seen, core.Hit{Record: rec})
	}
	return hits, nil
}

func AppendUniqueHit(limit int, hits []core.Hit, seen map[string]struct{}, hit core.Hit) []core.Hit {
	key := RecordKey(hit.Record)
	if _, ok := seen[key]; ok || (limit > 0 && len(hits) >= limit) {
		return hits
	}
	seen[key] = struct{}{}
	return append(hits, hit)
}

func RecordKey(rec core.Record) string {
	return strings.Join([]string{string(rec.Scope),
		strings.ToLower(strings.Join(strings.Fields(rec.Content), " "))}, "\x00")
}

// HitsPromptBlock renders hits as the "Relevant Memory" prompt block,
// de-duplicating by normalized content and prefixing each line with its
// provenance when present. When budgetChars > 0 the block content is truncated
// to that many bytes on a line boundary; budgetChars <= 0 means unlimited.
func HitsPromptBlock(hits []core.Hit, budgetChars int) core.PromptBlock {
	var out strings.Builder
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		content := strings.TrimSpace(strutil.FirstNonEmpty(hit.Record.Summary, hit.Record.Content))
		if content == "" {
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(content), " "))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out.WriteString("- ")
		if hit.Provenance != "" {
			out.WriteString("[")
			out.WriteString(hit.Provenance)
			out.WriteString("] ")
		}
		out.WriteString(content)
		out.WriteString("\n")
	}
	return core.PromptBlock{Title: "Relevant Memory", Priority: 150, Source: core.SourceSQLite,
		Content: truncateLines(strings.TrimSpace(out.String()), budgetChars)}
}

// truncateLines truncates s to at most budget bytes on a line boundary. A
// non-positive budget disables truncation.
func truncateLines(s string, budget int) string {
	if budget <= 0 || len(s) <= budget {
		return s
	}
	cut := strutil.TruncateUTF8(s, budget)
	if nl := strings.LastIndexByte(cut, '\n'); nl > 0 {
		cut = cut[:nl]
	}
	return strings.TrimSpace(cut)
}
