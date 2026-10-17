package zerfoo

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/memory/summary"
	"github.com/invokr-ai/invokr/config"
)

type fakeEmbed struct {
	lastInputs []string
	vectors    [][]float32
	err        error
	closed     bool
}

func (f *fakeEmbed) Embed(texts []string) ([][]float32, error) {
	f.lastInputs = texts
	if f.err != nil {
		return nil, f.err
	}
	if f.vectors != nil {
		return f.vectors, nil
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func (f *fakeEmbed) Close() error { f.closed = true; return nil }

type fakeGen struct {
	out    string
	err    error
	closed bool
}

func (f *fakeGen) Generate(_ context.Context, _ string, _ int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

func (f *fakeGen) Close() error { f.closed = true; return nil }

func newTestWorker(cfg config.ZerfooMemoryConfig, emb *fakeEmbed, gen *fakeGen) *Worker {
	return NewWorkerWithLoaders(cfg, Loaders{
		Embed: func(string) (embedModel, error) {
			if emb == nil {
				return nil, errors.New("no embed model")
			}
			return emb, nil
		},
		Gen: func(string) (genModel, error) {
			if gen == nil {
				return nil, errors.New("no gen model")
			}
			return gen, nil
		},
	})
}

func TestLazyLoadDoesNotLoadOnConstruction(t *testing.T) {
	loaded := false
	w := NewWorkerWithLoaders(config.ZerfooMemoryConfig{EmbeddingDimensions: 3}, Loaders{
		Embed: func(string) (embedModel, error) { loaded = true; return &fakeEmbed{}, nil },
		Gen:   func(string) (genModel, error) { loaded = true; return &fakeGen{}, nil },
	})
	if loaded {
		t.Fatal("models loaded before first use")
	}
	if _, err := w.EmbedDocuments(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if !loaded {
		t.Fatal("model not loaded on first use")
	}
}

func TestEmbedQueriesAppliesPrefixDocumentsDoNot(t *testing.T) {
	emb := &fakeEmbed{}
	w := newTestWorker(config.ZerfooMemoryConfig{EmbeddingDimensions: 3}, emb, nil)

	if _, err := w.EmbedQueries(context.Background(), []string{"how to test"}); err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if got := emb.lastInputs[0]; !strings.HasPrefix(got, queryPrefix) {
		t.Fatalf("query missing prefix: %q", got)
	}

	if _, err := w.EmbedDocuments(context.Background(), []string{"a fact"}); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if got := emb.lastInputs[0]; got != "a fact" {
		t.Fatalf("document should be raw, got %q", got)
	}
}

func TestEmbedDimensionMismatch(t *testing.T) {
	emb := &fakeEmbed{vectors: [][]float32{{1, 2}}}
	w := newTestWorker(config.ZerfooMemoryConfig{EmbeddingDimensions: 1024}, emb, nil)
	_, err := w.EmbedDocuments(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("expected dimension mismatch error, got %v", err)
	}
}

func TestEmbedRejectsNonFinite(t *testing.T) {
	inf := float32(math.Inf(1))
	emb := &fakeEmbed{vectors: [][]float32{{inf, 0, 0}}}
	w := newTestWorker(config.ZerfooMemoryConfig{EmbeddingDimensions: 3}, emb, nil)
	if _, err := w.EmbedDocuments(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected non-finite error")
	}
}

func TestEmbedLoadError(t *testing.T) {
	w := newTestWorker(config.ZerfooMemoryConfig{}, nil, nil)
	_, err := w.EmbedDocuments(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "load embedding model") {
		t.Fatalf("expected load error, got %v", err)
	}
}

func TestEmbedContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := newTestWorker(config.ZerfooMemoryConfig{EmbeddingDimensions: 3}, &fakeEmbed{}, nil)
	if _, err := w.EmbedDocuments(ctx, []string{"x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSummarizeParsesJSON(t *testing.T) {
	gen := &fakeGen{out: `{"summary":"user prefers terse plans","facts":[{"subject":"user","predicate":"prefers","object":"terse plans","confidence":0.9}],"should_store":true}`}
	w := newTestWorker(config.ZerfooMemoryConfig{}, nil, gen)
	got, err := w.Summarize(context.Background(), summary.StructuredSummaryInput{
		Events: []summary.MemoryEvent{{Role: "user", Content: "I like terse plans"}},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got.Summary != "user prefers terse plans" || len(got.Facts) != 1 {
		t.Fatalf("unexpected summary: %#v", got)
	}
}

func TestSummarizeRequiresEvents(t *testing.T) {
	w := newTestWorker(config.ZerfooMemoryConfig{}, nil, &fakeGen{out: "{}"})
	if _, err := w.Summarize(context.Background(), summary.StructuredSummaryInput{}); err == nil {
		t.Fatal("expected error for empty events")
	}
}

func TestCloseReleasesModels(t *testing.T) {
	emb := &fakeEmbed{}
	gen := &fakeGen{out: `{"summary":"x"}`}
	w := newTestWorker(config.ZerfooMemoryConfig{EmbeddingDimensions: 3}, emb, gen)
	_, _ = w.EmbedDocuments(context.Background(), []string{"x"})
	_, _ = w.Summarize(context.Background(), summary.StructuredSummaryInput{Events: []summary.MemoryEvent{{Content: "x"}}})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !emb.closed || !gen.closed {
		t.Fatal("models not closed")
	}
	if _, err := w.EmbedDocuments(context.Background(), []string{"x"}); !errors.Is(err, errClosed) {
		t.Fatalf("expected errClosed after Close, got %v", err)
	}
}
