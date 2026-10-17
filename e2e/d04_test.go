//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invokr-ai/invokr/capability/memory/markdown"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestD04_RetrievalIndexLifecycle(t *testing.T) {
	home := newHome(t, "")
	doc := filepath.Join(home.workspace, "docs", "d04.md")
	mustWrite(t, doc, "d04-project-41\n")
	mustWrite(t, filepath.Join(markdown.ProjectDirFor(home.state.MemoryDir, home.workspace), "MEMORY.md"), "- d04-memory-52\n")
	mustWrite(t, filepath.Join(filepath.Dir(home.workspace), "outside", "secret.md"), "outside-index-canary-88\n")

	refresh := managedToolStep("d04-refresh", "retrieval", map[string]any{
		"query": "d04-refresh-99", "scope": "project_docs", "max_results": 1,
	})
	var mu sync.Mutex
	var refreshErr error
	refresh.BeforeResponse = func() {
		// Rewrite the doc mid-session: the next retrieval call must pick up the
		// new content by re-syncing the doc record before searching.
		mu.Lock()
		defer mu.Unlock()
		refreshErr = os.WriteFile(doc, []byte("d04-project-41\nd04-refresh-99\n"), 0o600)
	}
	provider := providertest.NewScripted(t,
		managedToolStep("d04-doc", "retrieval", map[string]any{"query": "d04-project-41", "scope": "project_docs", "max_results": 1}),
		managedToolStep("d04-memory", "retrieval", map[string]any{"query": "d04-memory-52", "scope": "memory", "max_results": 1}),
		managedToolStep("d04-combined", "retrieval", map[string]any{"query": "d04", "scope": "project_docs_and_memory", "max_results": 2}),
		managedToolStep("d04-repeat", "retrieval", map[string]any{"query": "d04-project-41", "scope": "project_docs", "max_results": 1}),
		refresh,
		managedToolStep("d04-outside", "retrieval", map[string]any{"query": "outside-index-canary-88", "scope": "project_docs", "max_results": 1}),
		providertest.ReplyStep("done"),
	)
	defer provider.Close()
	rewriteConfig(t, &home, func(app *config.AppConfig) { app.Provider.BaseURL = provider.URL() })

	got := runInProc(t, home, provider.URL(), "exercise retrieval lifecycle", grantAll)

	if len(got.Resp.ToolFailures) != 0 {
		t.Fatalf("tool failures: %#v", got.Resp.ToolFailures)
	}
	results := toolResults(provider.Snapshot().CompletionRequests)
	for _, want := range []string{"docs/d04.md", "d04-memory-52", "d04-refresh-99"} {
		if !strings.Contains(results, want) {
			t.Errorf("retrieval results missing %q", want)
		}
	}
	for _, rec := range memoryRows(t, home) {
		if rec.Kind == "doc" && strings.Contains(rec.Content, "outside-index-canary-88") {
			t.Fatal("outside symlink target entered doc records")
		}
	}
	if strings.Contains(results, "outside-index-canary-88") {
		t.Fatal("outside symlink target returned by retrieval")
	}
	if paths := docRecordPaths(t, home); !slices.Contains(paths, "docs/d04.md") {
		t.Fatalf("doc records missing docs/d04.md: %#v", paths)
	}
	mu.Lock()
	defer mu.Unlock()
	if refreshErr != nil {
		t.Fatalf("refresh doc rewrite: %v", refreshErr)
	}
	if len(got.Approvals) != 1 {
		t.Fatalf("index-write approvals = %d, want one request-local grant", len(got.Approvals))
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
