//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

func TestC06_SearchAndRetrieval(t *testing.T) {
	// The passwd sub-check is a confinement regression guard: file_search must
	// not read /etc/passwd content through the etc-link -> /etc directory
	// symlink. The walker treats the symlink as a non-directory entry and never
	// descends into it, so the search returns zero hits. The assertion inspects
	// the tool RESULT (not the whole request) so it isn't fooled by the search
	// query string echoed in the tool-call arguments.
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("c06-search", "file_search", map[string]any{"query": "amber-orbit"}),
		managedToolStep("c06-retrieval", "retrieval", map[string]any{"query": "gateway memory module"}),
		providertest.NativeToolCallStep("c06-passwd", "file_search", map[string]any{"query": "root:x:", "mode": "literal"}),
		providertest.ReplyStep("done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	outsideCanary := "outside-index-canary-62"
	mustWrite(t, filepath.Join(filepath.Dir(home.workspace), "outside", "secret.md"), outsideCanary)

	// grantAll approves the retrieval_index_write needed on first index build.
	got := runInProc(t, home, provider.URL(), "search and retrieve", grantAll)

	if got.Resp.ToolCallCount != 4 {
		t.Fatalf("tool_call_count = %d, want 4", got.Resp.ToolCallCount)
	}
	if len(got.Resp.ToolFailures) != 0 {
		t.Fatalf("unexpected tool failures: %#v", got.Resp.ToolFailures)
	}
	if len(got.Approvals) != 1 || got.Approvals[0].Tool != "retrieval" {
		t.Fatalf("index-write approvals = %#v, want one retrieval approval", got.Approvals)
	}

	snapshot := provider.Snapshot()

	// file_search for "amber-orbit" must surface README.md.
	if !strings.Contains(toolResultForCall(snapshot.CompletionRequests, "c06-search"), "README.md") {
		t.Fatalf("file_search result for amber-orbit did not surface README.md in provider requests")
	}

	// retrieval must surface in-root (relative) paths only — docs/architecture.md
	// contains "gateway memory module" and is indexed as a relative path.
	if !strings.Contains(toolResultForCall(snapshot.CompletionRequests, "c06-retrieval"), "docs/architecture.md") {
		t.Fatalf("retrieval did not surface docs/architecture.md for query 'gateway memory module'")
	}

	// Confinement: the passwd search must return no hits — /etc/passwd content
	// must not be reachable through etc-link. Check the tool results only.
	if strings.Contains(toolResults(snapshot.CompletionRequests), "root:x:") {
		t.Fatalf("file_search escaped the workspace: /etc/passwd content reachable via etc-link symlink")
	}

	// Docs are mirrored into the memory store as doc-kind records (ADR 0015):
	// records must exist, must not contain outside sources, and must carry
	// workspace-relative paths only.
	paths := docRecordPaths(t, home)
	if len(paths) == 0 {
		t.Fatal("expected doc records in the memory store after retrieval")
	}
	for _, rec := range memoryRows(t, home) {
		if rec.Kind != "doc" {
			continue
		}
		if strings.Contains(rec.Content, outsideCanary) {
			t.Fatalf("doc record contains outside source content: %#v", rec)
		}
	}
	for _, path := range paths {
		if strings.Contains(path, "secret.md") {
			t.Fatalf("doc records include outside source %q", path)
		}
		if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) || strings.HasPrefix(path, "../") {
			t.Fatalf("doc record path escapes workspace: %q", path)
		}
	}

	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
