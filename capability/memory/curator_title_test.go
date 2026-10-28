package memory

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/capability/memory/summary"
	"github.com/invokr-agent/invokr/capability/sessionstore"
)

// fakeTitler satisfies both memorySummarizer and titler.
type fakeTitler struct {
	calls int
	err   error
}

func (f *fakeTitler) Summarize(context.Context, summary.StructuredSummaryInput) (summary.MemorySummary, error) {
	return summary.MemorySummary{}, nil
}

func (f *fakeTitler) Title(_ context.Context, lines []string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return "Generated title", nil
}

func newTitleCurator(t *testing.T, gen memorySummarizer, noTitles bool) (*curator, *sessionstore.Store) {
	t.Helper()
	store := sessionstore.New(t.TempDir())
	if _, err := store.Append(context.Background(), "s1", "user", "how do trees work"); err != nil {
		t.Fatal(err)
	}
	c := newCurator(TieredManager{sessions: store, summarizer: gen, noTitles: noTitles})
	c.startWorker()
	return c, store
}

func TestCloseTitlesCuratedSessions(t *testing.T) {
	gen := &fakeTitler{}
	c, store := newTitleCurator(t, gen, false)
	c.queueTitle("s1")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := store.Title("s1"); got != "Generated title" {
		t.Fatalf("title = %q, want %q", got, "Generated title")
	}
	if gen.calls != 1 {
		t.Fatalf("title calls = %d, want 1", gen.calls)
	}
}

func TestCloseSkipsSessionsThatAlreadyHaveTitles(t *testing.T) {
	gen := &fakeTitler{}
	c, store := newTitleCurator(t, gen, false)
	if err := store.SetTitle("s1", "Existing"); err != nil {
		t.Fatal(err)
	}
	c.queueTitle("s1")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if gen.calls != 0 {
		t.Fatalf("title calls = %d, want 0 — an existing title must not be regenerated", gen.calls)
	}
	if got := store.Title("s1"); got != "Existing" {
		t.Fatalf("title = %q, want it left alone", got)
	}
}

func TestCloseSkipsTitlingWhenDisabled(t *testing.T) {
	gen := &fakeTitler{}
	c, store := newTitleCurator(t, gen, true)
	c.queueTitle("s1")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if gen.calls != 0 {
		t.Fatalf("title calls = %d, want 0 when titling is disabled", gen.calls)
	}
	if got := store.Title("s1"); got != "" {
		t.Fatalf("title = %q, want none", got)
	}
}

// A summarizer with no Title method (the zerfoo worker, whose CPU decode is far
// too slow to title) must not break the run.
func TestCloseWithoutTitlerIsHarmless(t *testing.T) {
	c, store := newTitleCurator(t, nil, false)
	c.queueTitle("s1")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := store.Title("s1"); got != "" {
		t.Fatalf("title = %q, want none", got)
	}
}
