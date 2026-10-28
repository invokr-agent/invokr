package recall

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/capability/memory/policy"
	"github.com/invokr-agent/invokr/config"
)

type fakePinnedSource struct {
	blocks  core.PromptBlocks
	records []core.Record
}

func (s fakePinnedSource) PromptBlocks(context.Context, core.PromptRequest) (core.PromptBlocks, error) {
	return s.blocks, nil
}

func (s fakePinnedSource) Records(context.Context, core.Query) ([]core.Record, error) {
	return s.records, nil
}

type fakeLexicalSource struct {
	hits []core.Hit
}

func (s fakeLexicalSource) Recall(context.Context, core.Query) ([]core.Hit, error) {
	return s.hits, nil
}

func TestOrchestratorPromptBlocksDelegatesToPinnedSource(t *testing.T) {
	want := core.PromptBlocks{{Title: "Project Memory", Content: "Pinned", Source: core.SourceMarkdown}}
	o := NewOrchestrator(fakePinnedSource{blocks: want}, nil, policy.Default)

	got, err := o.PromptBlocks(context.Background(), core.PromptRequest{})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}
	if len(got) != 1 || got[0].Content != "Pinned" {
		t.Fatalf("PromptBlocks() = %#v, want %#v", got, want)
	}
}

func TestOrchestratorPromptBlocksCanIncludeTier2Hits(t *testing.T) {
	pol := policy.Default
	pol.Tier2.PromptEnabled = true
	o := NewOrchestrator(
		fakePinnedSource{blocks: core.PromptBlocks{{Title: "Project Memory", Content: "Pinned", Source: core.SourceMarkdown}}},
		fakeLexicalSource{hits: []core.Hit{{Record: core.Record{
			ID:      "sqlite-id",
			Scope:   config.ScopeProject,
			Content: "SQLite prompt memory.",
			Source:  core.SourceSQLite,
		}}}},
		pol,
	)

	got, err := o.PromptBlocks(context.Background(), core.PromptRequest{UserMessage: "prompt memory"})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("PromptBlocks() length = %d, want 2: %#v", len(got), got)
	}
	if got[1].Title != "Relevant Memory" || got[1].Source != core.SourceSQLite || got[1].Content != "- SQLite prompt memory." {
		t.Fatalf("unexpected tier2 block: %#v", got[1])
	}
}

func TestHitsPromptBlockMarksCrossSessionProvenance(t *testing.T) {
	block := HitsPromptBlock([]core.Hit{{Record: core.Record{Content: "prior deployment detail"}, Provenance: "cross-session context"}}, 0)
	if block.Content != "- [cross-session context] prior deployment detail" {
		t.Fatalf("prompt block = %q", block.Content)
	}
}

func TestHitsPromptBlockTruncatesOnLineBoundary(t *testing.T) {
	hits := []core.Hit{
		{Record: core.Record{Content: "first line of memory"}},
		{Record: core.Record{Content: "second line of memory"}},
		{Record: core.Record{Content: "third line of memory"}},
	}
	// A budget that lands mid-second-line must cut back to the newline, keeping
	// only whole lines.
	block := HitsPromptBlock(hits, 30)
	if block.Content != "- first line of memory" {
		t.Fatalf("truncated block = %q", block.Content)
	}
	// A non-positive budget disables truncation.
	full := HitsPromptBlock(hits, 0)
	if full.Content != "- first line of memory\n- second line of memory\n- third line of memory" {
		t.Fatalf("unbudgeted block = %q", full.Content)
	}
}

func TestOrchestratorPromptBlocksEnforcesBudget(t *testing.T) {
	pol := policy.Default
	pol.Tier2.PromptEnabled = true
	const budget = 80
	o := NewOrchestrator(
		fakePinnedSource{blocks: core.PromptBlocks{{Title: "Project Memory", Content: strings.Repeat("p", 30), Source: core.SourceMarkdown}}},
		fakeLexicalSource{hits: []core.Hit{{Record: core.Record{
			Content: strings.Repeat("q", 200),
			Source:  core.SourceSQLite,
		}}}},
		pol,
	)

	got, err := o.PromptBlocks(context.Background(), core.PromptRequest{UserMessage: "prompt memory", BudgetChars: budget})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}
	total := 0
	joined := ""
	for _, block := range got {
		total += len(block.Content)
		joined += block.Content
	}
	if total > budget {
		t.Fatalf("total content length = %d, want <= %d", total, budget)
	}
	if !strings.Contains(joined, core.MemoryTruncationMarker) {
		t.Fatalf("expected truncation marker, got %q", joined)
	}
}

func TestOrchestratorPromptBlocksSkipsTier2ByDefault(t *testing.T) {
	o := NewOrchestrator(
		fakePinnedSource{blocks: core.PromptBlocks{{Title: "Project Memory", Content: "Pinned", Source: core.SourceMarkdown}}},
		fakeLexicalSource{hits: []core.Hit{{Record: core.Record{Content: "SQLite prompt memory.", Source: core.SourceSQLite}}}},
		policy.Default,
	)

	got, err := o.PromptBlocks(context.Background(), core.PromptRequest{UserMessage: "prompt memory"})
	if err != nil {
		t.Fatalf("PromptBlocks() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("PromptBlocks() length = %d, want default pinned only: %#v", len(got), got)
	}
}

func TestOrchestratorRecallMergesAndDedupesPinnedWithSQLiteWinning(t *testing.T) {
	sqliteHit := core.Hit{Record: core.Record{
		ID:      "sqlite-id",
		Scope:   config.ScopeProject,
		Kind:    core.KindDecision,
		Content: "Shared memory.",
		Source:  core.SourceSQLite,
	}}
	pinned := []core.Record{
		{ID: "Shared memory.", Scope: config.ScopeProject, Content: "Shared memory.", Source: core.SourceMarkdown},
		{ID: "Pinned only.", Scope: config.ScopeProject, Content: "Pinned only.", Source: core.SourceMarkdown},
	}
	o := NewOrchestrator(fakePinnedSource{records: pinned}, fakeLexicalSource{hits: []core.Hit{sqliteHit}}, policy.Default)

	got, err := o.Recall(context.Background(), core.Query{Query: "memory", Limit: 5})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recall() length = %d, want 2: %#v", len(got), got)
	}
	if got[0].Record.ID != "sqlite-id" || got[1].Record.ID != "Pinned only." {
		t.Fatalf("Recall() order/dedupe = %#v", got)
	}
}
