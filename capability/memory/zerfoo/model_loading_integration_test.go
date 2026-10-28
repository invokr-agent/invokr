//go:build integration

// Isolated integration tests for the zerfoo memory model subsystems. Unlike
// worker_integration_test.go (which pins the default LiquidAI models and their
// 1024-dim embeddings), these are model-agnostic: they exercise the loading and
// inference machinery against whatever local GGUF files are provided, so they
// can run against any embedding/chat model on the developer's machine.
//
// Run, pointing at local GGUF files (no network download):
//
//	INVOKR_ZERFOO_INTEGRATION=1 \
//	  INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH=/path/embed.gguf \
//	  INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH=/path/chat.gguf \
//	  go test -tags=integration -race -run Loading ./internal/memory/zerfoo/...
package zerfoo

import (
	"context"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/invokr-agent/invokr/capability/memory/summary"
	"github.com/invokr-agent/invokr/config"
)

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("INVOKR_ZERFOO_INTEGRATION") != "1" {
		t.Skip("set INVOKR_ZERFOO_INTEGRATION=1 to run model-loading integration tests")
	}
}

// requireModelEnv returns the configured path and model ID for a role
// ("EMBEDDING" or "SUMMARIZATION"), skipping if neither is set, so a model can
// be given either as a local GGUF path or a HuggingFace model ID.
func requireModelEnv(t *testing.T, role string) (path, model string) {
	t.Helper()
	requireIntegration(t)
	path = os.Getenv("INVOKR_MEMORY_ZERFOO_" + role + "_PATH")
	model = os.Getenv("INVOKR_MEMORY_ZERFOO_" + role + "_MODEL")
	if path == "" && model == "" {
		t.Skipf("set INVOKR_MEMORY_ZERFOO_%s_PATH or _MODEL to a GGUF path or HuggingFace ID", role)
	}
	return path, model
}

// requestTimeoutMS returns the configured request timeout, defaulting high so
// slow CPU-only boxes can finish prefill/generation during evaluation.
func requestTimeoutMS() int {
	if v, err := strconv.Atoi(os.Getenv("INVOKR_MEMORY_ZERFOO_REQUEST_TIMEOUT_MS")); err == nil && v > 0 {
		return v
	}
	return 900000
}

// localEmbeddingConfig points the worker at a local embedding GGUF and disables
// the fixed-dimension check so the worker accepts the model's native width: the
// goal is to test loading/inference, not a specific model's dimensionality.
//
// EmbeddingDimensions is set to -1 rather than 0 because WithDefaults() (called
// again inside NewWorker) rewrites 0 back to the default; a negative value
// survives and disables validation (worker.validate only enforces when > 0).
func localEmbeddingConfig(t *testing.T) config.ZerfooMemoryConfig {
	t.Helper()
	path, model := requireModelEnv(t, "EMBEDDING")
	cfg := config.ZerfooMemoryConfig{Enabled: true, EmbeddingPath: path, EmbeddingModel: model}.WithDefaults()
	cfg.EmbeddingDimensions = -1
	return cfg
}

// TestIntegrationEmbeddingLoading loads a real embedding model and verifies it
// produces consistent, finite, unit-norm vectors — proving the load + inference
// path works end to end in isolation from the summarization subsystem.
func TestIntegrationEmbeddingLoading(t *testing.T) {
	w := NewWorker(localEmbeddingConfig(t))
	defer w.Close()

	docs := []string{
		"The capital of France is Paris.",
		"Goroutines are lightweight threads managed by the Go runtime.",
	}
	vecs, err := w.EmbedDocuments(context.Background(), docs)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(vecs) != len(docs) {
		t.Fatalf("got %d vectors, want %d", len(vecs), len(docs))
	}
	dim := len(vecs[0])
	if dim == 0 {
		t.Fatal("embedding has zero dimensions")
	}
	t.Logf("embedding dimension = %d", dim)
	for i, v := range vecs {
		if len(v) != dim {
			t.Fatalf("vector %d width = %d, want %d (inconsistent)", i, len(v), dim)
		}
		for _, f := range v {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				t.Fatalf("vector %d has a non-finite value", i)
			}
		}
		if n := l2Norm(v); math.Abs(n-1.0) > 1e-3 {
			t.Fatalf("vector %d not unit-norm: |v|=%f", i, n)
		}
	}
}

// TestIntegrationConcurrentEmbeddingLoadsOnce hammers the worker with concurrent
// embedding requests and asserts the model is loaded exactly once. This exercises
// the per-model load semaphore (no duplicate GGUF loads) and inference mutex (no
// concurrent inference). Run with -race to catch data races.
func TestIntegrationConcurrentEmbeddingLoadsOnce(t *testing.T) {
	cfg := localEmbeddingConfig(t)

	var loads int64
	real := defaultLoaders()
	loaders := Loaders{
		Embed: func(ref string) (embedModel, error) {
			atomic.AddInt64(&loads, 1)
			return real.Embed(ref)
		},
		Gen: real.Gen,
	}
	w := NewWorkerWithLoaders(cfg, loaders)
	defer w.Close()

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.EmbedDocuments(context.Background(), []string{"concurrent load probe"}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent EmbedDocuments: %v", err)
	}
	if got := atomic.LoadInt64(&loads); got != 1 {
		t.Fatalf("embedding model loaded %d times, want 1", got)
	}
}

// TestIntegrationSummarizationLoading loads a real chat/generation model and
// verifies summarization produces non-empty output, in isolation from the
// embedding subsystem.
func TestIntegrationSummarizationLoading(t *testing.T) {
	path, model := requireModelEnv(t, "SUMMARIZATION")
	cfg := config.ZerfooMemoryConfig{Enabled: true, SummarizationPath: path, SummarizationModel: model}.WithDefaults()
	cfg.MaxSummaryTokens = 128
	cfg.RequestTimeoutMS = requestTimeoutMS()

	w := NewWorker(cfg)
	defer w.Close()

	out, err := w.Summarize(context.Background(), summary.StructuredSummaryInput{Events: []summary.MemoryEvent{
		{Role: "user", Content: "We chose Postgres as the datastore for the billing service."},
		{Role: "assistant", Content: "Noted: Postgres for billing."},
	}})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(out.Summary) == 0 && len(out.Facts) == 0 {
		t.Fatal("summarization produced no summary and no facts")
	}
}
