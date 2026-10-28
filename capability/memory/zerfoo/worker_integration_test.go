//go:build integration

// Integration tests that load real Zerfoo GGUF models. They are excluded from
// the default build (`go test ./...`) by the `integration` build tag and are
// additionally gated on INVOKR_ZERFOO_INTEGRATION=1 so they only run when the
// models are available or downloadable.
//
// Run with:
//
//	INVOKR_ZERFOO_INTEGRATION=1 go test -tags=integration ./internal/memory/zerfoo/...
//
// Optionally point at local GGUF files to avoid network downloads:
//
//	INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH=/path/embed.gguf
//	INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH=/path/sum.gguf
//
// With neither set, the default model IDs are downloaded on the fly.
package zerfoo

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/memory/summary"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil"
)

func syntheticTranscript() summary.StructuredSummaryInput {
	return summary.StructuredSummaryInput{Events: []summary.MemoryEvent{
		{Role: "user", Content: "We decided to use Postgres as the primary datastore for the billing service."},
		{Role: "assistant", Content: "Noted. Postgres for billing. Anything else?"},
		{Role: "user", Content: "Also the weather is nice today, but unrelated. Remember we deploy on Fridays."},
	}}
}

func factsText(s summary.MemorySummary) string {
	var b strings.Builder
	for _, f := range s.Facts {
		b.WriteString(f.Subject + " " + f.Predicate + " " + f.Object + " ")
	}
	return b.String()
}

func integrationConfig(t *testing.T) config.ZerfooMemoryConfig {
	t.Helper()
	if os.Getenv("INVOKR_ZERFOO_INTEGRATION") != "1" {
		t.Skip("set INVOKR_ZERFOO_INTEGRATION=1 to run Zerfoo model integration tests")
	}
	testutil.UseRepoModelCache(t)
	cfg := config.ZerfooMemoryConfig{
		Enabled:            true,
		EmbeddingPath:      os.Getenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH"),
		SummarizationPath:  os.Getenv("INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH"),
		EmbeddingModel:     os.Getenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_MODEL"),
		SummarizationModel: os.Getenv("INVOKR_MEMORY_ZERFOO_SUMMARIZATION_MODEL"),
	}
	// An explicit env path or model ID wins; otherwise WithDefaults supplies the
	// documented default model IDs, which are downloaded on first use.
	return cfg.WithDefaults()
}

func l2Norm(v []float32) float64 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	return math.Sqrt(sum)
}

func TestIntegrationEmbeddingModel(t *testing.T) {
	cfg := integrationConfig(t)
	w := NewWorker(cfg)
	defer w.Close()

	docs := []string{
		"The capital of France is Paris.",
		"Goroutines are lightweight threads managed by the Go runtime.",
		"Espresso is a concentrated coffee brewed under pressure.",
	}
	ctx := context.Background()
	vecs, err := w.EmbedDocuments(ctx, docs)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(vecs) != len(docs) {
		t.Fatalf("got %d vectors, want %d", len(vecs), len(docs))
	}
	for i, v := range vecs {
		if len(v) != config.DefaultZerfooEmbeddingDims {
			t.Fatalf("vector %d dim = %d, want %d", i, len(v), config.DefaultZerfooEmbeddingDims)
		}
		for _, f := range v {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				t.Fatalf("vector %d has non-finite value", i)
			}
		}
		if n := l2Norm(v); math.Abs(n-1.0) > 1e-3 {
			t.Fatalf("vector %d not unit-norm: |v|=%f", i, n)
		}
	}

	qvecs, err := w.EmbedQueries(ctx, []string{"What is the capital city of France?"})
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	// The France query should rank the France document highest.
	best, bestScore := -1, float32(-2)
	for i, dv := range vecs {
		s := cosine(qvecs[0], dv)
		if s > bestScore {
			best, bestScore = i, s
		}
	}
	if best != 0 {
		t.Fatalf("expected France doc (index 0) to rank highest, got index %d", best)
	}
}

func cosine(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

func TestIntegrationSummarizationModel(t *testing.T) {
	cfg := integrationConfig(t)
	cfg.MaxSummaryTokens = 200
	w := NewWorker(cfg)
	defer w.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	out, err := w.Summarize(ctx, syntheticTranscript())
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if strings.TrimSpace(out.Summary) == "" {
		t.Fatal("empty summary")
	}
	lower := strings.ToLower(out.Summary + " " + factsText(out))
	if !strings.Contains(lower, "postgres") {
		t.Fatalf("summary missing key fact (postgres): %q", out.Summary)
	}
	if len(out.Summary) > 4000 {
		t.Fatalf("summary unexpectedly long: %d chars", len(out.Summary))
	}
}
