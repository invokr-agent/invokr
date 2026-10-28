//go:build e2e

package e2e

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/memory"
	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/capability/memory/summary"
	memzerfoo "github.com/invokr-agent/invokr/capability/memory/zerfoo"
	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
)

func TestLiveD05_ModelBackedMemory(t *testing.T) {
	if os.Getenv("INVOKR_E2E_LIVE") != "1" {
		t.Skip("LIVE-ONLY: set INVOKR_E2E_LIVE=1")
	}
	zcfg := d05Config(t)
	// Production summarizes via the main chat provider, not an in-process GGUF
	// (chat_summarizer.go: zerfoo CPU decode is non-viable). Mirror that here by
	// summarizing through the live model; embeddings stay on the zerfoo worker.
	liveURL := strings.TrimRight(os.Getenv("INVOKR_E2E_LIVE_URL"), "/")
	liveModel := strings.TrimSpace(os.Getenv("INVOKR_E2E_LIVE_MODEL"))
	if liveURL == "" || liveModel == "" {
		t.Skip("BLOCKED: set INVOKR_E2E_LIVE_URL and INVOKR_E2E_LIVE_MODEL to summarize via the live model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dir := t.TempDir()
	app := config.AppConfig{Memory: config.MemoryConfig{Dir: dir, Zerfoo: zcfg}}
	worker := memzerfoo.NewWorker(zcfg)
	var workerClosed bool
	t.Cleanup(func() {
		if !workerClosed {
			worker.Close()
		}
	})
	mgr, err := memory.NewManagerFromConfigWithProvider(app, events.NewOrderedDispatcher(nil), worker, nil, nil)
	if err != nil {
		t.Skipf("BLOCKED: initialize model-backed memory: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	cold := time.Now()
	if _, err := mgr.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Source: core.SourceRuntime,
		Content: "The billing service uses Postgres as its primary datastore."}); err != nil {
		t.Skipf("BLOCKED: load embedding model: %v", err)
	}
	coldLatency := time.Since(cold)
	warm := time.Now()
	if _, err := mgr.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Source: core.SourceRuntime,
		Content: "The frontend release train runs every Friday afternoon."}); err != nil {
		t.Fatalf("warm embedding: %v", err)
	}
	warmLatency := time.Since(warm)

	prompt, err := summary.RenderSummaryPrompt(summary.StructuredSummaryInput{Events: []summary.MemoryEvent{
		{Role: "user", Content: "We chose Postgres for billing because transactions must be durable."},
		{Role: "assistant", Content: "Recorded the Postgres billing decision."},
	}})
	if err != nil {
		t.Fatalf("render summary prompt: %v", err)
	}
	raw, err := clients.NewClient(liveURL).Summarize(ctx, clients.SummaryInput{Prompt: prompt, JSON: true, Model: liveModel})
	if err != nil {
		t.Skipf("BLOCKED: live-model summarization: %v", err)
	}
	generated, err := summary.ParseMemorySummary(raw)
	if err != nil {
		t.Fatalf("parse live summary: %v", err)
	}
	if strings.TrimSpace(generated.Summary) == "" {
		t.Fatal("live summarizer returned an empty summary")
	}
	if _, err := mgr.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindSummary, Source: core.SourceRuntime,
		Content: generated.Summary}); err != nil {
		t.Fatalf("persist generated summary: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	worker.Close()
	workerClosed = true

	restartWorker := memzerfoo.NewWorker(zcfg)
	defer restartWorker.Close()
	restarted, err := memory.NewManagerFromConfigWithProvider(app, events.NewOrderedDispatcher(nil), restartWorker, nil, nil)
	if err != nil {
		t.Fatalf("restart memory manager: %v", err)
	}
	defer restarted.Close()
	recallStarted := time.Now()
	hits, err := restarted.Recall(ctx, core.Query{Query: "Which relational engine powers invoicing?", Limit: 10})
	if err != nil {
		t.Fatalf("semantic recall after restart: %v", err)
	}
	var found bool
	for _, hit := range hits {
		found = found || strings.Contains(strings.ToLower(hit.Record.Content), "postgres")
	}
	if !found {
		t.Fatalf("semantic lexical-miss query did not recall Postgres; hits=%v", hits)
	}
	t.Logf("OBSERVATION: cold_embed=%s warm_embed=%s restart_recall=%s hits=%d summary_facts=%d",
		coldLatency, warmLatency, time.Since(recallStarted), len(hits), len(generated.Facts))
}

func d05Config(t *testing.T) config.ZerfooMemoryConfig {
	t.Helper()
	embedPath := os.Getenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH")
	embedModel := os.Getenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_MODEL")
	if embedPath == "" && embedModel == "" {
		t.Skip("BLOCKED: cached embedding GGUF is not configured")
	}
	// Summarization no longer needs a GGUF: it runs through the live chat model.
	cfg := config.ZerfooMemoryConfig{Enabled: true, EmbeddingPath: embedPath, EmbeddingModel: embedModel}.WithDefaults()
	if dimensions, err := strconv.Atoi(os.Getenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_DIMENSIONS")); err == nil && dimensions > 0 {
		cfg.EmbeddingDimensions = dimensions
	} else {
		cfg.EmbeddingDimensions = -1
	}
	return cfg
}
