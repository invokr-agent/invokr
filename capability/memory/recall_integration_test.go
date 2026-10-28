//go:build integration

// End-to-end memory recall integration test using the real Zerfoo runtime.
// Gated by the `integration` build tag and INVOKR_ZERFOO_INTEGRATION=1.
//
//	INVOKR_ZERFOO_INTEGRATION=1 go test -tags=integration ./internal/memory/...
package memory

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/memory/core"
	memzerfoo "github.com/invokr-agent/invokr/capability/memory/zerfoo"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/support/testutil"
)

func TestIntegrationEndToEndRecall(t *testing.T) {
	if os.Getenv("INVOKR_ZERFOO_INTEGRATION") != "1" {
		t.Skip("set INVOKR_ZERFOO_INTEGRATION=1 to run Zerfoo recall integration test")
	}
	testutil.UseRepoModelCache(t)
	ctx := context.Background()
	dir := t.TempDir()

	zcfg := config.ZerfooMemoryConfig{
		Enabled:            true,
		EmbeddingPath:      os.Getenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH"),
		SummarizationPath:  os.Getenv("INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH"),
		EmbeddingModel:     os.Getenv("INVOKR_MEMORY_ZERFOO_EMBEDDING_MODEL"),
		SummarizationModel: os.Getenv("INVOKR_MEMORY_ZERFOO_SUMMARIZATION_MODEL"),
	}
	// With no explicit path or model ID, WithDefaults names the documented
	// default models, downloaded into the repo model cache on first run.
	zcfg = zcfg.WithDefaults()

	worker := memzerfoo.NewWorker(zcfg)
	app := config.AppConfig{Memory: config.MemoryConfig{Dir: dir, Zerfoo: zcfg}}

	mgr, err := NewManagerFromConfigWithWorker(app, events.NewOrderedDispatcher(nil), worker)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}

	notes := []string{
		"The billing service uses Postgres as its primary datastore.",
		"The frontend is built with React and TypeScript.",
		"Deployments happen every Friday afternoon.",
	}
	for _, n := range notes {
		if _, err := mgr.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindSummary, Source: core.SourceRuntime, Content: n}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	// Reopen the store to prove persistence across restart.
	if err := mgr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	worker2 := memzerfoo.NewWorker(zcfg)
	defer worker2.Close()
	mgr2, err := NewManagerFromConfigWithWorker(app, events.NewOrderedDispatcher(nil), worker2)
	if err != nil {
		t.Fatalf("reopen manager: %v", err)
	}

	hits, err := mgr2.Recall(ctx, core.Query{Query: "which database does billing use?", Limit: 3})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no recall hits")
	}
	if !strings.Contains(strings.ToLower(hits[0].Record.Content), "postgres") {
		t.Fatalf("expected Postgres note ranked first, got %q", hits[0].Record.Content)
	}
}

// TestFallbackToFTSWhenVectorDisabled verifies lexical recall still works with
// no Zerfoo worker configured (offline, default fast test — no build tag needed
// would be ideal, but this file is integration-tagged; the equivalent offline
// behavior is covered by the recall package unit tests).
func TestFallbackToFTSWhenVectorDisabled(t *testing.T) {
	ctx := context.Background()
	app := config.AppConfig{Memory: config.MemoryConfig{Dir: t.TempDir()}}
	mgr, err := NewManagerFromConfigWithEvents(app, events.NewOrderedDispatcher(nil))
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	defer mgr.Close()
	if _, err := mgr.Save(ctx, core.Record{Scope: config.ScopeProject, Kind: core.KindFact, Source: core.SourceRuntime, Content: "lexical recall works without vectors"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	hits, err := mgr.Recall(ctx, core.Query{Query: "lexical", Limit: 5})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected FTS fallback hit")
	}
}
