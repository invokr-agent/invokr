package recallgate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/config"
)

type fakeCompleter struct {
	out       string
	err       error
	gotPrompt string
	gotMax    int
}

func (f *fakeCompleter) Summarize(_ context.Context, input clients.SummaryInput) (string, error) {
	f.gotPrompt = input.Prompt
	f.gotMax = input.MaxTokens
	return f.out, f.err
}

type fakeRecaller struct {
	byQuery map[string][]core.Hit
	err     error
	calls   []string
}

func (f *fakeRecaller) Recall(_ context.Context, req core.Query) ([]core.Hit, error) {
	f.calls = append(f.calls, req.Query)
	if f.err != nil {
		return nil, f.err
	}
	return f.byQuery[req.Query], nil
}

func hit(content string) core.Hit {
	return core.Hit{Record: core.Record{Content: content}}
}

func testConfig() config.RecallGateConfig {
	return config.RecallGateConfig{
		Enabled: true, NothinkSuffix: "/no_think", MaxQueries: 3,
		PerQueryLimit: 5, MaxHits: 8, BudgetChars: 2000, MinMessageChars: 12, TimeoutMS: 10000,
	}
}

func newGate(c completer, r recaller) *Gate {
	return New(testConfig(), c, r, nil, nil)
}

func TestBlockReturnsHitsWhenRecallTrue(t *testing.T) {
	comp := &fakeCompleter{out: `{"recall": true, "queries": ["alpha", "beta"]}`}
	rec := &fakeRecaller{byQuery: map[string][]core.Hit{
		"alpha": {hit("fact one")},
		"beta":  {hit("fact two")},
	}}
	block, ok := newGate(comp, rec).Block(context.Background(), "s1", "what did we decide about alpha")
	if !ok {
		t.Fatalf("expected block present")
	}
	if block.Title != "Relevant Memory" {
		t.Fatalf("title = %q", block.Title)
	}
	if !strings.Contains(block.Content, "fact one") || !strings.Contains(block.Content, "fact two") {
		t.Fatalf("content missing hits: %q", block.Content)
	}
	if comp.gotMax != 96 {
		t.Fatalf("max tokens = %d, want 96", comp.gotMax)
	}
}

func TestBlockDedupesAcrossQueries(t *testing.T) {
	comp := &fakeCompleter{out: `{"recall": true, "queries": ["a", "b"]}`}
	rec := &fakeRecaller{byQuery: map[string][]core.Hit{
		"a": {hit("same fact")},
		"b": {hit("same fact")},
	}}
	block, ok := newGate(comp, rec).Block(context.Background(), "s1", "long enough message")
	if !ok {
		t.Fatalf("expected block")
	}
	if got := strings.Count(block.Content, "same fact"); got != 1 {
		t.Fatalf("expected dedupe, got %d occurrences", got)
	}
}

func TestBlockCapsAtMaxHits(t *testing.T) {
	cfg := testConfig()
	cfg.MaxHits = 2
	comp := &fakeCompleter{out: `{"recall": true, "queries": ["a"]}`}
	rec := &fakeRecaller{byQuery: map[string][]core.Hit{
		"a": {hit("one"), hit("two"), hit("three")},
	}}
	block, ok := New(cfg, comp, rec, nil, nil).Block(context.Background(), "s1", "long enough message")
	if !ok {
		t.Fatalf("expected block")
	}
	if strings.Contains(block.Content, "three") {
		t.Fatalf("expected cap at 2 hits, got %q", block.Content)
	}
}

func TestBlockBudgetTruncation(t *testing.T) {
	cfg := testConfig()
	cfg.BudgetChars = 20
	comp := &fakeCompleter{out: `{"recall": true, "queries": ["a"]}`}
	rec := &fakeRecaller{byQuery: map[string][]core.Hit{
		"a": {hit("first line here"), hit("second line here"), hit("third line here")},
	}}
	block, _ := New(cfg, comp, rec, nil, nil).Block(context.Background(), "s1", "long enough message")
	if len(block.Content) > 20 {
		t.Fatalf("content exceeds budget: %d bytes %q", len(block.Content), block.Content)
	}
}

func TestBlockFalseWhenRecallFalse(t *testing.T) {
	comp := &fakeCompleter{out: `{"recall": false}`}
	rec := &fakeRecaller{}
	if _, ok := newGate(comp, rec).Block(context.Background(), "s1", "long enough message"); ok {
		t.Fatalf("expected no block")
	}
	if len(rec.calls) != 0 {
		t.Fatalf("recall should not run when recall=false")
	}
}

func TestBlockFalseWhenDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Enabled = false
	comp := &fakeCompleter{out: `{"recall": true, "queries": ["a"]}`}
	if _, ok := New(cfg, comp, &fakeRecaller{}, nil, nil).Block(context.Background(), "s1", "long enough message"); ok {
		t.Fatalf("expected no block when disabled")
	}
}

func TestBlockFalseWhenMessageTooShort(t *testing.T) {
	comp := &fakeCompleter{out: `{"recall": true, "queries": ["a"]}`}
	if _, ok := newGate(comp, &fakeRecaller{}).Block(context.Background(), "s1", "hi"); ok {
		t.Fatalf("expected no block for short message")
	}
	if comp.gotPrompt != "" {
		t.Fatalf("probe should not fire for short message")
	}
}

func TestBlockFalseOnProbeError(t *testing.T) {
	comp := &fakeCompleter{err: errors.New("boom")}
	if _, ok := newGate(comp, &fakeRecaller{}).Block(context.Background(), "s1", "long enough message"); ok {
		t.Fatalf("expected no block on probe error")
	}
}

func TestBlockFalseOnGarbage(t *testing.T) {
	comp := &fakeCompleter{out: "not json at all"}
	if _, ok := newGate(comp, &fakeRecaller{}).Block(context.Background(), "s1", "long enough message"); ok {
		t.Fatalf("expected no block on garbage")
	}
}

func TestBlockFalseOnEmptyHits(t *testing.T) {
	comp := &fakeCompleter{out: `{"recall": true, "queries": ["a"]}`}
	rec := &fakeRecaller{byQuery: map[string][]core.Hit{}}
	if _, ok := newGate(comp, rec).Block(context.Background(), "s1", "long enough message"); ok {
		t.Fatalf("expected no block on empty hits")
	}
}

func TestBlockFalseOnRecallError(t *testing.T) {
	comp := &fakeCompleter{out: `{"recall": true, "queries": ["a"]}`}
	rec := &fakeRecaller{err: errors.New("db down")}
	if _, ok := newGate(comp, rec).Block(context.Background(), "s1", "long enough message"); ok {
		t.Fatalf("expected no block on recall error")
	}
}

func TestParseDecisionForgiving(t *testing.T) {
	cfg := testConfig()
	cases := []struct {
		name, raw string
		recall    bool
	}{
		{"fenced", "```json\n{\"recall\": true, \"queries\": [\"x\"]}\n```", true},
		{"think prefix", "<think>reasoning</think>\n{\"recall\": true, \"queries\": [\"x\"]}", true},
		{"prose wrap", "Sure: {\"recall\": false} done", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := parseDecision(tc.raw, cfg, "msg")
			if err != nil {
				t.Fatalf("parse err: %v", err)
			}
			if d.Recall != tc.recall {
				t.Fatalf("recall = %v, want %v", d.Recall, tc.recall)
			}
		})
	}
}

func TestParseDecisionFallsBackToUserMessage(t *testing.T) {
	d, err := parseDecision(`{"recall": true, "queries": []}`, testConfig(), "  the user message  ")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(d.Queries) != 1 || d.Queries[0] != "the user message" {
		t.Fatalf("expected fallback query, got %v", d.Queries)
	}
}

func TestClampQueriesCapsAndTrims(t *testing.T) {
	got := clampQueries([]string{" a ", "", "b", "c", "d"}, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %v", got)
	}
	if got[0] != "a" {
		t.Fatalf("expected trimmed, got %q", got[0])
	}
}
