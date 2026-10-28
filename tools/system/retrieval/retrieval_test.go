package retrieval_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/capability/memory"
	memorycore "github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/support/testutil"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/retrieval"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

// fakeMemory is a storing memory.Manager double. It mirrors the sqlite store's
// contract the retrieval tool depends on: Save assigns ids, List/Recall filter
// by kind, and an empty kind filter excludes doc records.
type fakeMemory struct {
	records     []memorycore.Record
	nextID      int
	saves       int
	updates     int
	metaUpdates int
	deletes     int
}

func (m *fakeMemory) Close() error { return nil }
func (m *fakeMemory) PromptBlocks(context.Context, memorycore.PromptRequest) (memorycore.PromptBlocks, error) {
	return nil, nil
}

func (m *fakeMemory) Save(_ context.Context, rec memorycore.Record) (memorycore.Record, error) {
	m.saves++
	m.nextID++
	rec.ID = fmt.Sprintf("rec-%d", m.nextID)
	rec.CreatedAt = time.Now().UTC()
	rec.UpdatedAt = rec.CreatedAt
	m.records = append(m.records, rec)
	return rec, nil
}

func (m *fakeMemory) Update(_ context.Context, id string, rec memorycore.Record) (memorycore.Record, error) {
	m.updates++
	for i := range m.records {
		if m.records[i].ID != id {
			continue
		}
		if rec.Content != "" {
			m.records[i].Content = rec.Content
		}
		if rec.Metadata != nil {
			m.records[i].Metadata = rec.Metadata
		}
		m.records[i].UpdatedAt = time.Now().UTC()
		return m.records[i], nil
	}
	return memorycore.Record{}, fmt.Errorf("record %q not found", id)
}

func (m *fakeMemory) UpdateMetadata(_ context.Context, id string, metadata map[string]string) error {
	m.metaUpdates++
	for i := range m.records {
		if m.records[i].ID == id {
			m.records[i].Metadata = metadata
			return nil
		}
	}
	return fmt.Errorf("record %q not found", id)
}

func (m *fakeMemory) Delete(_ context.Context, id string) error {
	m.deletes++
	m.records = slices.DeleteFunc(m.records, func(rec memorycore.Record) bool { return rec.ID == id })
	return nil
}

func (m *fakeMemory) List(_ context.Context, filter memorycore.Query) ([]memorycore.Record, error) {
	var out []memorycore.Record
	for _, rec := range m.records {
		if kindAllowed(rec.Kind, filter.Kinds) {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (m *fakeMemory) Recall(_ context.Context, req memorycore.Query) ([]memorycore.Hit, error) {
	terms := strings.Fields(strings.ToLower(req.Query))
	var hits []memorycore.Hit
	for _, rec := range m.records {
		if !kindAllowed(rec.Kind, req.Kinds) {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{rec.Content, rec.Summary}, "\n"))
		if recallMatches(haystack, terms) {
			hits = append(hits, memorycore.Hit{Record: rec, Score: 0.5})
		}
		if req.Limit > 0 && len(hits) >= req.Limit {
			break
		}
	}
	return hits, nil
}

func (m *fakeMemory) SearchArchive(context.Context, string, int) ([]memorycore.Record, error) {
	return nil, nil
}

func (m *fakeMemory) RestoreArchived(context.Context, []string) ([]string, error) {
	return nil, nil
}

func (m *fakeMemory) docRecords() []memorycore.Record {
	var out []memorycore.Record
	for _, rec := range m.records {
		if rec.Kind == memorycore.KindDoc {
			out = append(out, rec)
		}
	}
	return out
}

// kindAllowed mirrors the sqlite store's default kind filter: an empty kind
// list excludes doc records; otherwise the record kind must be listed.
func kindAllowed(kind memorycore.Kind, kinds []memorycore.Kind) bool {
	if len(kinds) == 0 {
		return kind != memorycore.KindDoc
	}
	return slices.Contains(kinds, kind)
}

func recallMatches(haystack string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			return true
		}
	}
	return false
}

type captureDispatcher struct {
	events []events.Event
}

func (d *captureDispatcher) Dispatch(_ context.Context, event events.Event) {
	d.events = append(d.events, event)
}

type captureApprover struct {
	requests []approval.Request
}

func (a *captureApprover) RequestApproval(_ context.Context, req approval.Request) (approval.Decision, error) {
	a.requests = append(a.requests, req)
	return approval.Decision{Approved: true}, nil
}

func TestRetrievalRegistersAndSearchesDocsAndMemory(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "Toolbox plugins load from toolbox directories.\n")
	mustWriteFile(t, filepath.Join(root, "docs", "memory.txt"), "Memory docs mention comet recall.\n")
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n// toolbox source should not be indexed\n")
	mem := &fakeMemory{records: []memorycore.Record{{
		ID:      "mem-1",
		Scope:   config.ScopeProject,
		Kind:    memorycore.KindFact,
		Content: "Project memory stores alpha decisions.",
	}}}
	dispatcher := &captureDispatcher{}
	caller := newRetrievalCaller(t, root, mem, dispatcher)

	if !slices.ContainsFunc(caller.Specs(), func(spec core.ToolSpec) bool { return spec.Name == retrieval.ToolName }) {
		t.Fatal("expected retrieval tool registered")
	}

	docs := callRetrieval(t, caller, `{"query":"toolbox plugins","scope":"project_docs","max_results":5}`)
	assertRetrievalHit(t, docs, "file", "README.md")
	assertNoRetrievalHit(t, docs, "file", "main.go")

	memoryResp := callRetrieval(t, caller, `{"query":"alpha decisions","scope":"memory","max_results":5}`)
	assertRetrievalHit(t, memoryResp, "memory", "mem-1")

	combined := callRetrieval(t, caller, `{"query":"comet alpha","scope":"project_docs_and_memory","max_results":5}`)
	assertRetrievalHit(t, combined, "file", "docs/memory.txt")
	assertRetrievalHit(t, combined, "memory", "mem-1")

	docRecords := mem.docRecords()
	if len(docRecords) != 2 {
		t.Fatalf("expected 2 doc records in memory store, got %#v", docRecords)
	}
	for _, rec := range docRecords {
		if rec.Scope != config.ScopeProject || rec.Metadata["path"] == "" || rec.Metadata["source_hash"] == "" {
			t.Fatalf("doc record missing scope or metadata: %#v", rec)
		}
	}

	for _, ev := range dispatcher.events {
		if ev.Audit == nil {
			continue
		}
		data, _ := json.Marshal(ev.Audit)
		if strings.Contains(string(data), "toolbox plugins") || strings.Contains(string(data), "alpha decisions") {
			t.Fatalf("audit leaked raw query: %s", string(data))
		}
	}
	assertAuditAction(t, dispatcher.events, retrieval.ActionSearch)
	assertAuditAction(t, dispatcher.events, retrieval.ActionIndex)
}

func TestRetrievalFirstIndexRequiresApprovalOnce(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "Approval gated doc content.\n")
	approver := &captureApprover{}
	caller := newRetrievalCallerWithApprover(t, root, &fakeMemory{}, nil, approver)

	callRetrieval(t, caller, `{"query":"approval","scope":"project_docs"}`)
	if len(approver.requests) != 1 || approver.requests[0].Tier != retrieval.IndexWritePolicyTier {
		t.Fatalf("expected one index-write approval, got %#v", approver.requests)
	}

	callRetrieval(t, caller, `{"query":"approval","scope":"project_docs"}`)
	if len(approver.requests) != 1 {
		t.Fatalf("expected no further approvals once docs are indexed, got %#v", approver.requests)
	}
}

func TestRetrievalMemoryScopeDoesNotSyncDocs(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "docs only content\n")
	mem := &fakeMemory{}
	caller := newRetrievalCaller(t, root, mem, nil)

	callRetrieval(t, caller, `{"query":"anything","scope":"memory","max_results":5}`)
	if mem.saves != 0 {
		t.Fatalf("memory-scope search should not index docs, saves=%d", mem.saves)
	}
}

func TestRetrievalReindexesChangedAndDeletedDocs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	other := filepath.Join(root, "docs", "extra.md")
	mustWriteFile(t, path, "original needle content\n")
	mustWriteFile(t, other, "second document body\n")
	mem := &fakeMemory{}
	caller := newRetrievalCaller(t, root, mem, nil)

	callRetrieval(t, caller, `{"query":"needle","scope":"project_docs"}`)
	if mem.saves != 2 {
		t.Fatalf("expected both docs saved, saves=%d", mem.saves)
	}

	// A rewrite with different content must update the same record.
	time.Sleep(5 * time.Millisecond)
	mustWriteFile(t, path, "rewritten banana content\n")
	resp := callRetrieval(t, caller, `{"query":"banana","scope":"project_docs"}`)
	assertRetrievalHit(t, resp, "file", "README.md")
	if mem.updates == 0 {
		t.Fatalf("expected changed doc to update its record, updates=%d", mem.updates)
	}
	if got := len(mem.docRecords()); got != 2 {
		t.Fatalf("expected 2 doc records after update, got %d", got)
	}

	// A deleted file must drop its record.
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	resp = callRetrieval(t, caller, `{"query":"second document","scope":"project_docs"}`)
	assertNoRetrievalHit(t, resp, "file", "docs/extra.md")
	if mem.deletes != 1 || len(mem.docRecords()) != 1 {
		t.Fatalf("expected deleted doc pruned, deletes=%d records=%d", mem.deletes, len(mem.docRecords()))
	}
}

func TestRetrievalLazyLargeWorkspaceRequiresExplicitIndexAndReturnsStale(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 101; i++ {
		mustWriteFile(t, filepath.Join(root, fmt.Sprintf("docs/%03d.txt", i)), fmt.Sprintf("needle-%03d\n", i))
	}
	mem := &fakeMemory{}
	app := config.AppConfig{Toolbox: config.ToolboxConfig{Dir: t.TempDir()}, Workspace: config.WorkspaceConfig{Root: root}}
	caller := tools.NewCaller(core.CallerDeps{App: app, Memory: mem})
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      retrieval.ToolName,
		Arguments: json.RawMessage(`{"query":"needle-001"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected first-index rejection without approver, got %v", err)
	}

	service := newRetrievalService(t, root, mem, nil)
	if _, err := service.BuildIndex(context.Background(), retrieval.BuildOptions{AllowLarge: true}); err != nil {
		t.Fatalf("explicit build: %v", err)
	}
	if got := len(mem.docRecords()); got != 101 {
		t.Fatalf("expected 101 doc records after explicit build, got %d", got)
	}

	large := strings.Repeat("x", 5<<20+1)
	mustWriteFile(t, filepath.Join(root, "docs/001.txt"), "needle-001\n"+large)

	resp := callRetrieval(t, caller, `{"query":"needle-001","max_results":1}`)
	if len(resp.Hits) != 1 || !resp.Hits[0].Stale {
		t.Fatalf("expected stale hit for oversized changed doc, got %#v", resp.Hits)
	}

	mustWriteFile(t, filepath.Join(root, "docs/new-small.txt"), "brand-new-small-token\n")
	resp = callRetrieval(t, caller, `{"query":"brand-new-small-token","max_results":1}`)
	if len(resp.Hits) != 1 || resp.Hits[0].PathOrID != "docs/new-small.txt" || resp.Hits[0].Stale {
		t.Fatalf("expected fresh hit for new small doc, got %#v", resp.Hits)
	}
}

func TestRetrievalExplicitIndexAudit(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "AGENTS.md"), "Agent retrieval audit note.\n")
	dispatcher := &captureDispatcher{}
	service := newRetrievalService(t, root, &fakeMemory{}, dispatcher)

	if _, err := service.BuildIndex(context.Background(), retrieval.BuildOptions{}); err != nil {
		t.Fatalf("build index: %v", err)
	}
	assertAuditAction(t, dispatcher.events, retrieval.ActionIndex)
}

func TestRetrievalUsesStoredDocsForRepeatedSearch(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "Cached retrieval query.\n")
	dispatcher := &captureDispatcher{}
	mem := &fakeMemory{}
	caller := newRetrievalCaller(t, root, mem, dispatcher)

	callRetrieval(t, caller, `{"query":"cached"}`)
	indexAudits := countAuditAction(dispatcher.events, retrieval.ActionIndex)
	if indexAudits == 0 {
		t.Fatal("expected first search to index docs")
	}
	saves := mem.saves

	callRetrieval(t, caller, `{"query":"cached"}`)
	if got := countAuditAction(dispatcher.events, retrieval.ActionIndex); got != indexAudits {
		t.Fatalf("expected unchanged second search without extra index audit, got before=%d after=%d", indexAudits, got)
	}
	if mem.saves != saves || mem.updates != 0 {
		t.Fatalf("expected no re-writes for unchanged docs, saves=%d updates=%d", mem.saves, mem.updates)
	}
}

func TestRetrievalSnippetPreservesUTF8(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("é", 241) + " needle\n"
	mustWriteFile(t, filepath.Join(root, "README.md"), line)
	caller := newRetrievalCaller(t, root, &fakeMemory{}, nil)

	resp := callRetrieval(t, caller, `{"query":"needle","max_results":1}`)
	if len(resp.Hits) != 1 {
		t.Fatalf("expected hit, got %#v", resp.Hits)
	}
	if !utf8.ValidString(resp.Hits[0].Snippet) {
		t.Fatalf("snippet is invalid utf8: %q", resp.Hits[0].Snippet)
	}
}

func TestRetrievalSkipsProblemFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteFile(t, outside, "outside secret\n")
	mustWriteFile(t, filepath.Join(root, "README.md"), "reachable docs stay indexed\n")
	mustWriteFile(t, filepath.Join(root, "docs", "invalid.txt"), string([]byte{0xff, 0xfe, 0xfd}))
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs", "outside.txt")); err != nil {
		t.Fatal(err)
	}
	caller := newRetrievalCaller(t, root, &fakeMemory{}, nil)

	resp := callRetrieval(t, caller, `{"query":"reachable","scope":"project_docs","max_results":5}`)
	assertRetrievalHit(t, resp, "file", "README.md")
	resp = callRetrieval(t, caller, `{"query":"outside secret","scope":"project_docs","max_results":5}`)
	if len(resp.Hits) != 0 {
		t.Fatalf("expected symlink target skipped, got %#v", resp.Hits)
	}
}

func TestRetrievalMatchesSnakeCaseTokens(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "Use retrieval_index_write for index writes.\n")
	caller := newRetrievalCaller(t, root, &fakeMemory{}, nil)

	resp := callRetrieval(t, caller, `{"query":"retrieval_index_write","scope":"project_docs","max_results":1}`)
	if len(resp.Hits) != 1 || resp.Hits[0].PathOrID != "README.md" {
		t.Fatalf("expected snake_case hit, got %#v", resp.Hits)
	}
}

func TestRetrievalMemoryScopeReturnsOnlyMemoryHits(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "Alpha appears in project docs too.\n")
	mem := &fakeMemory{records: []memorycore.Record{{
		ID:      "mem-1",
		Scope:   config.ScopeProject,
		Kind:    memorycore.KindFact,
		Content: "Project memory stores alpha decisions.",
	}}}
	caller := newRetrievalCaller(t, root, mem, nil)

	resp := callRetrieval(t, caller, `{"query":"alpha","scope":"memory","max_results":5}`)
	if len(resp.Hits) == 0 {
		t.Fatal("expected memory hits")
	}
	for _, hit := range resp.Hits {
		if hit.SourceType != "memory" {
			t.Fatalf("memory scope returned non-memory hit: %#v", hit)
		}
		if hit.MemoryRecordID == nil || *hit.MemoryRecordID != "mem-1" {
			t.Fatalf("expected memory_record_id mem-1, got %#v", hit)
		}
	}
	assertRetrievalHit(t, resp, "memory", "mem-1")
}

func TestRetrievalProjectDocsScopeExcludesMemory(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "Alpha shows up in docs.\n")
	mem := &fakeMemory{records: []memorycore.Record{{
		ID:      "mem-1",
		Scope:   config.ScopeProject,
		Kind:    memorycore.KindFact,
		Content: "Project memory stores alpha decisions.",
	}}}
	caller := newRetrievalCaller(t, root, mem, nil)

	resp := callRetrieval(t, caller, `{"query":"alpha","scope":"project_docs","max_results":5}`)
	if len(resp.Hits) == 0 {
		t.Fatal("expected file hits")
	}
	for _, hit := range resp.Hits {
		if hit.SourceType != "file" {
			t.Fatalf("project_docs scope returned non-file hit: %#v", hit)
		}
	}
	assertRetrievalHit(t, resp, "file", "README.md")
}

func TestRetrievalCombinedScopeReturnsFileAndMemoryHits(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "Shared keyword lives in the docs.\n")
	mem := &fakeMemory{records: []memorycore.Record{{
		ID:      "mem-1",
		Scope:   config.ScopeProject,
		Kind:    memorycore.KindFact,
		Content: "Shared keyword also lives in memory.",
	}}}
	caller := newRetrievalCaller(t, root, mem, nil)

	resp := callRetrieval(t, caller, `{"query":"keyword","scope":"project_docs_and_memory","max_results":5}`)
	assertRetrievalHit(t, resp, "file", "README.md")
	assertRetrievalHit(t, resp, "memory", "mem-1")
	assertHitShape(t, resp)
}

func TestRetrievalCombinedScopeRespectsMaxResults(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		mustWriteFile(t, filepath.Join(root, fmt.Sprintf("doc-%d.md", i)), fmt.Sprintf("keyword in file %d\n", i))
	}
	mem := &fakeMemory{records: []memorycore.Record{
		{ID: "mem-1", Scope: config.ScopeProject, Kind: memorycore.KindFact, Content: "keyword in memory one"},
		{ID: "mem-2", Scope: config.ScopeProject, Kind: memorycore.KindFact, Content: "keyword in memory two"},
	}}
	caller := newRetrievalCaller(t, root, mem, nil)

	resp := callRetrieval(t, caller, `{"query":"keyword","scope":"project_docs_and_memory","max_results":2}`)
	if len(resp.Hits) == 0 || len(resp.Hits) > 2 {
		t.Fatalf("expected 1..2 hits under max_results=2, got %d: %#v", len(resp.Hits), resp.Hits)
	}
}

func TestRetrievalEmptyMemoryScopeReturnsNoHitsWithoutError(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "keyword appears in docs only.\n")
	mem := &fakeMemory{}
	caller := newRetrievalCaller(t, root, mem, nil)

	resp := callRetrieval(t, caller, `{"query":"nothing-matches-this","scope":"memory","max_results":5}`)
	if len(resp.Hits) != 0 {
		t.Fatalf("expected empty memory scope to yield no hits, got %#v", resp.Hits)
	}
}

// TestRetrievalAgainstRealMemoryStore exercises the whole path against the
// real sqlite-backed manager: FTS recall, doc-kind isolation, and per-file doc
// identity (two byte-identical files stay two records, and editing one to
// equal another does not collide).
func TestRetrievalAgainstRealMemoryStore(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "docs", "a.md"), "Shared gateway body.\n")
	mustWriteFile(t, filepath.Join(root, "docs", "b.md"), "Shared gateway body.\n")
	mustWriteFile(t, filepath.Join(root, "docs", "c.md"), "Distinct beacon body.\n")

	app := config.AppConfig{
		Workspace: config.WorkspaceConfig{Root: root},
		Memory:    config.MemoryConfig{Dir: t.TempDir()},
	}
	manager, err := memory.NewManagerFromConfigWithProvider(app, events.NewOrderedDispatcher(nil), nil, nil, nil)
	if err != nil {
		t.Fatalf("build memory manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	service := retrieval.NewServiceWithApprover(app, manager, nil,
		testutil.StaticApprover{Decision: approval.Decision{Approved: true}},
		workspace.NewWorkspacePolicy(app.Workspace))

	search := func(query string) retrieval.Response {
		t.Helper()
		out, err := service.RunToolArgs(context.Background(), retrieval.Args{Query: query, Scope: "project_docs", MaxResults: 10})
		if err != nil {
			t.Fatalf("retrieval %q: %v", query, err)
		}
		var resp retrieval.Response
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("decode retrieval output: %v", err)
		}
		return resp
	}

	resp := search("gateway")
	assertRetrievalHit(t, resp, "file", "docs/a.md")
	assertRetrievalHit(t, resp, "file", "docs/b.md")

	// Edit c.md to be byte-identical to the other two: it must update in place
	// and remain independently retrievable.
	mustWriteFile(t, filepath.Join(root, "docs", "c.md"), "Shared gateway body.\n")
	resp = search("gateway")
	for _, path := range []string{"docs/a.md", "docs/b.md", "docs/c.md"} {
		assertRetrievalHit(t, resp, "file", path)
	}
	for _, hit := range resp.Hits {
		if hit.Stale {
			t.Fatalf("unexpected stale hit after identical-content edit: %#v", hit)
		}
	}

	docs, err := manager.List(context.Background(), memorycore.Query{
		Kinds: []memorycore.Kind{memorycore.KindDoc},
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("list doc records: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("doc records = %d, want 3: %#v", len(docs), docs)
	}

	// Doc records must stay out of default (kind-less) recall.
	hits, err := manager.Recall(context.Background(), memorycore.Query{Query: "gateway", Limit: 10})
	if err != nil {
		t.Fatalf("default recall: %v", err)
	}
	for _, hit := range hits {
		if hit.Record.Kind == memorycore.KindDoc {
			t.Fatalf("default recall surfaced a doc record: %#v", hit.Record)
		}
	}
}

// assertHitShape pins the response JSON contract without asserting scores or
// ordering: every hit carries non-empty path_or_id, snippet, and source_type,
// and memory hits carry a memory_record_id.
func assertHitShape(t *testing.T, resp retrieval.Response) {
	t.Helper()
	for _, hit := range resp.Hits {
		if hit.PathOrID == "" {
			t.Fatalf("hit missing path_or_id: %#v", hit)
		}
		if hit.Snippet == "" {
			t.Fatalf("hit missing snippet: %#v", hit)
		}
		if hit.SourceType == "" {
			t.Fatalf("hit missing source_type: %#v", hit)
		}
		if hit.SourceType == "memory" && (hit.MemoryRecordID == nil || *hit.MemoryRecordID == "") {
			t.Fatalf("memory hit missing memory_record_id: %#v", hit)
		}
	}
}

func newRetrievalCaller(t *testing.T, root string, manager *fakeMemory, dispatcher events.Dispatcher) *tools.Caller {
	t.Helper()
	return newRetrievalCallerWithApprover(t, root, manager, dispatcher,
		testutil.StaticApprover{Decision: approval.Decision{Approved: true}})
}

func newRetrievalCallerWithApprover(t *testing.T, root string, manager *fakeMemory, dispatcher events.Dispatcher, approver approval.Approver) *tools.Caller {
	t.Helper()
	app := config.AppConfig{
		Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
		Workspace: config.WorkspaceConfig{Root: root},
	}
	return tools.NewCaller(core.CallerDeps{
		App: app, Memory: manager, Dispatcher: dispatcher, Approver: approver,
	})
}

func newRetrievalService(t *testing.T, root string, manager *fakeMemory, dispatcher events.Dispatcher) *retrieval.Service {
	t.Helper()
	app := config.AppConfig{Workspace: config.WorkspaceConfig{Root: root}}
	return retrieval.NewServiceWithApprover(
		app, manager, dispatcher,
		testutil.StaticApprover{Decision: approval.Decision{Approved: true}},
		workspace.NewWorkspacePolicy(app.Workspace),
	)
}

func callRetrieval(t *testing.T, caller *tools.Caller, args string) retrieval.Response {
	t.Helper()
	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      retrieval.ToolName,
		Arguments: json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("retrieval failed: %v", err)
	}
	var resp retrieval.Response
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("decode retrieval output %q: %v", output, err)
	}
	return resp
}

func assertRetrievalHit(t *testing.T, resp retrieval.Response, sourceType string, pathOrID string) {
	t.Helper()
	for _, hit := range resp.Hits {
		if hit.SourceType == sourceType && hit.PathOrID == pathOrID {
			return
		}
	}
	t.Fatalf("expected %s %s in hits %#v", sourceType, pathOrID, resp.Hits)
}

func assertNoRetrievalHit(t *testing.T, resp retrieval.Response, sourceType string, pathOrID string) {
	t.Helper()
	for _, hit := range resp.Hits {
		if hit.SourceType == sourceType && hit.PathOrID == pathOrID {
			t.Fatalf("did not expect %s %s in hits %#v", sourceType, pathOrID, resp.Hits)
		}
	}
}

func assertAuditAction(t *testing.T, got []events.Event, action string) {
	t.Helper()
	for _, ev := range got {
		if ev.Audit != nil && ev.Audit.Action == action {
			return
		}
	}
	t.Fatalf("expected audit action %s in %#v", action, got)
}

func countAuditAction(got []events.Event, action string) int {
	count := 0
	for _, ev := range got {
		if ev.Audit != nil && ev.Audit.Action == action {
			count++
		}
	}
	return count
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Two workspaces share one user-global memory.db (Memory.Dir defaults to
// ~/.invokr), which the per-workspace .invokr/index/index.json never did. This
// pins the workspace discriminator: no cross-repo hits, no cross-repo pruning,
// no collision between identical relative paths, and one approval prompt per
// fresh workspace.
func TestRetrievalScopesDocRecordsPerWorkspaceInSharedStore(t *testing.T) {
	memDir := t.TempDir()
	rootA, rootB := t.TempDir(), t.TempDir()
	// Same relative path, different content, plus a token unique to each repo.
	mustWriteFile(t, filepath.Join(rootA, "README.md"), "shared-token alpha-only-marker in repo A.\n")
	mustWriteFile(t, filepath.Join(rootB, "README.md"), "shared-token beta-only-marker in repo B.\n")
	mustWriteFile(t, filepath.Join(rootA, "docs", "a-only.md"), "shared-token alpha extra doc.\n")

	manager, err := memory.NewManagerFromConfigWithProvider(
		config.AppConfig{Memory: config.MemoryConfig{Dir: memDir}},
		events.NewOrderedDispatcher(nil), nil, nil, nil)
	if err != nil {
		t.Fatalf("build memory manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	approverA, approverB := &captureApprover{}, &captureApprover{}
	newService := func(root string, approver approval.Approver) *retrieval.Service {
		app := config.AppConfig{
			Workspace: config.WorkspaceConfig{Root: root},
			Memory:    config.MemoryConfig{Dir: memDir},
		}
		return retrieval.NewServiceWithApprover(app, manager, nil, approver,
			workspace.NewWorkspacePolicy(app.Workspace))
	}
	search := func(svc *retrieval.Service, query string) retrieval.Response {
		t.Helper()
		out, err := svc.RunToolArgs(context.Background(), retrieval.Args{Query: query, Scope: "project_docs", MaxResults: 10})
		if err != nil {
			t.Fatalf("retrieval %q: %v", query, err)
		}
		var resp retrieval.Response
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("decode retrieval output: %v", err)
		}
		return resp
	}
	docPaths := func(root string) []string {
		t.Helper()
		records, err := manager.List(context.Background(), memorycore.Query{
			Kinds:    []memorycore.Kind{memorycore.KindDoc},
			Metadata: map[string]string{memorycore.MetadataKeyWorkspace: filepath.ToSlash(root)},
			Limit:    100,
		})
		if err != nil {
			t.Fatalf("list doc records: %v", err)
		}
		var paths []string
		for _, rec := range records {
			paths = append(paths, rec.Metadata[memorycore.MetadataKeyPath])
		}
		slices.Sort(paths)
		return paths
	}

	serviceA, serviceB := newService(rootA, approverA), newService(rootB, approverB)

	// Indexing A prompts once for the index write.
	respA := search(serviceA, "shared-token")
	assertRetrievalHit(t, respA, "file", "README.md")
	assertRetrievalHit(t, respA, "file", "docs/a-only.md")
	if len(approverA.requests) != 1 {
		t.Fatalf("workspace A approvals = %d, want 1: %#v", len(approverA.requests), approverA.requests)
	}

	// A fresh workspace must prompt too, even though the shared store already
	// holds another workspace's docs.
	respB := search(serviceB, "shared-token")
	if len(approverB.requests) != 1 {
		t.Fatalf("workspace B approvals = %d, want 1 per fresh workspace: %#v", len(approverB.requests), approverB.requests)
	}

	// No leakage: B's search sees only B's README, with B's content.
	assertRetrievalHit(t, respB, "file", "README.md")
	assertNoRetrievalHit(t, respB, "file", "docs/a-only.md")
	for _, hit := range respB.Hits {
		if strings.Contains(hit.Snippet, "alpha-only-marker") {
			t.Fatalf("workspace B search leaked workspace A content: %#v", hit)
		}
	}

	// No path collision: identical relative paths are two distinct records.
	if got := docPaths(rootA); !slices.Equal(got, []string{"README.md", "docs/a-only.md"}) {
		t.Fatalf("workspace A doc records = %#v", got)
	}
	if got := docPaths(rootB); !slices.Equal(got, []string{"README.md"}) {
		t.Fatalf("workspace B doc records = %#v", got)
	}

	// No cross-prune: syncing B must not have deleted A's records, and A still
	// reads its own content back.
	respA = search(serviceA, "shared-token")
	assertRetrievalHit(t, respA, "file", "docs/a-only.md")
	foundA := false
	for _, hit := range respA.Hits {
		if hit.PathOrID == "README.md" && strings.Contains(hit.Snippet, "alpha-only-marker") {
			foundA = true
		}
		if strings.Contains(hit.Snippet, "beta-only-marker") {
			t.Fatalf("workspace A search leaked workspace B content: %#v", hit)
		}
	}
	if !foundA {
		t.Fatalf("workspace A README lost its own content after workspace B synced: %#v", respA.Hits)
	}
	if got := docPaths(rootA); !slices.Equal(got, []string{"README.md", "docs/a-only.md"}) {
		t.Fatalf("workspace A doc records after B sync = %#v", got)
	}

	// Re-indexing an unchanged workspace prompts no further approvals.
	if len(approverA.requests) != 1 {
		t.Fatalf("workspace A approvals after re-search = %d, want 1", len(approverA.requests))
	}
}

// A granted approval for a large build must yield a whole build, not a partial
// one truncated by the incremental byte budget.
func TestRetrievalApprovedLargeBuildIndexesEveryDoc(t *testing.T) {
	root := t.TempDir()
	// Comfortably past both lazy thresholds: 120 files, ~6 MB total.
	body := strings.Repeat("needle padding ", 3500)
	for i := 0; i < 120; i++ {
		mustWriteFile(t, filepath.Join(root, "docs", fmt.Sprintf("%03d.md", i)), fmt.Sprintf("doc-%03d %s\n", i, body))
	}
	mem := &fakeMemory{}
	approver := &captureApprover{}
	app := config.AppConfig{Workspace: config.WorkspaceConfig{Root: root}}
	service := retrieval.NewServiceWithApprover(app, mem, nil, approver,
		workspace.NewWorkspacePolicy(app.Workspace))

	// Seed the store first, so the follow-up build is not the "initial" one —
	// that is the case where a granted approval used to yield a partial build.
	if _, err := service.BuildIndex(context.Background(), retrieval.BuildOptions{AllowLarge: true}); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	if got := len(mem.docRecords()); got != 120 {
		t.Fatalf("seed indexed %d docs, want 120", got)
	}
	if len(approver.requests) != 0 {
		t.Fatalf("--allow-large should not prompt, got %#v", approver.requests)
	}

	// Change every doc, then rebuild without --allow-large: the build is large,
	// so it prompts, and the granted approval must cover the whole corpus.
	for i := 0; i < 120; i++ {
		mustWriteFile(t, filepath.Join(root, "docs", fmt.Sprintf("%03d.md", i)), fmt.Sprintf("doc-%03d rewritten %s\n", i, body))
	}
	result, err := service.BuildIndex(context.Background(), retrieval.BuildOptions{})
	if err != nil {
		t.Fatalf("build index: %v", err)
	}
	if len(approver.requests) != 1 {
		t.Fatalf("large build approvals = %d, want 1: %#v", len(approver.requests), approver.requests)
	}
	if result.SourceCount != 120 {
		t.Fatalf("source count = %d, want 120", result.SourceCount)
	}
	if mem.updates != 120 {
		t.Fatalf("re-indexed %d docs after approval, want all 120 — the byte budget truncated an approved build", mem.updates)
	}
}

// A content-identical rewrite (checkout, formatter pass, touch) must not cost a
// re-embed: the stored content hash gates the store write.
func TestRetrievalDoesNotRewriteContentIdenticalDocs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	mustWriteFile(t, path, "stable body text\n")
	mem := &fakeMemory{}
	caller := newRetrievalCaller(t, root, mem, nil)

	callRetrieval(t, caller, `{"query":"stable","scope":"project_docs"}`)
	if mem.saves != 1 {
		t.Fatalf("expected one save on first index, got %d", mem.saves)
	}

	// Rewrite byte-identical content with a fresh mtime: the metadata probe
	// misses, so the file is re-read, but the hash gate must stop the write.
	time.Sleep(5 * time.Millisecond)
	mustWriteFile(t, path, "stable body text\n")
	resp := callRetrieval(t, caller, `{"query":"stable","scope":"project_docs"}`)
	assertRetrievalHit(t, resp, "file", "README.md")
	if mem.updates != 0 || mem.saves != 1 {
		t.Fatalf("content-identical rewrite triggered a store write: saves=%d updates=%d", mem.saves, mem.updates)
	}

	// A real content change still writes.
	mustWriteFile(t, path, "changed body text\n")
	callRetrieval(t, caller, `{"query":"changed","scope":"project_docs"}`)
	if mem.updates != 1 {
		t.Fatalf("expected one update for changed content, got %d", mem.updates)
	}
}

// The record's hash and line count must describe the content the record holds
// (the store trims on write), so a hit's source_hash hashes what it exposes and
// its line range matches the trimmed body.
func TestRetrievalHitMetadataMatchesTrimmedContent(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "README.md"), "\n\n\nfirst line\nsecond needle line\n\n\n")
	mem := &fakeMemory{}
	caller := newRetrievalCaller(t, root, mem, nil)

	resp := callRetrieval(t, caller, `{"query":"needle","scope":"project_docs","max_results":1}`)
	if len(resp.Hits) != 1 {
		t.Fatalf("expected one hit, got %#v", resp.Hits)
	}
	hit := resp.Hits[0]
	docs := mem.docRecords()
	if len(docs) != 1 {
		t.Fatalf("expected one doc record, got %#v", docs)
	}
	stored := docs[0].Content
	if want := "sha256:" + strutil.HashHex(stored); hit.SourceHash != want {
		t.Fatalf("source_hash = %q, want %q (hash of the content the hit exposes)", hit.SourceHash, want)
	}
	if hit.LineStart != 1 || hit.LineEnd != 2 {
		t.Fatalf("line range = %d..%d, want 1..2 for the trimmed body", hit.LineStart, hit.LineEnd)
	}
}

// A byte-identical rewrite must refresh the size/mtime probe even though it
// writes no content, so the file is not re-read and re-hashed on every later
// sync — which would charge the incremental byte budget forever and crowd out
// genuinely changed files.
func TestRetrievalRefreshesProbeMetadataAfterIdenticalRewrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	mustWriteFile(t, path, "stable body text\n")
	mem := &fakeMemory{}
	caller := newRetrievalCaller(t, root, mem, nil)

	callRetrieval(t, caller, `{"query":"stable","scope":"project_docs"}`)
	if mem.saves != 1 {
		t.Fatalf("expected one save on first index, got %d", mem.saves)
	}

	// Byte-identical rewrite with a fresh mtime: no content write, but the probe
	// must be brought up to date.
	time.Sleep(5 * time.Millisecond)
	mustWriteFile(t, path, "stable body text\n")
	callRetrieval(t, caller, `{"query":"stable","scope":"project_docs"}`)
	if mem.updates != 0 || mem.saves != 1 {
		t.Fatalf("identical rewrite wrote content: saves=%d updates=%d", mem.saves, mem.updates)
	}
	if mem.metaUpdates != 1 {
		t.Fatalf("expected the probe metadata to be refreshed once, got %d", mem.metaUpdates)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	docs := mem.docRecords()
	if len(docs) != 1 {
		t.Fatalf("expected one doc record, got %#v", docs)
	}
	if got := docs[0].Metadata["file_mod_time"]; got != info.ModTime().UTC().Format(time.RFC3339Nano) {
		t.Fatalf("stored mtime = %q, want the file's current mtime %q", got, info.ModTime().UTC().Format(time.RFC3339Nano))
	}

	// With the probe current, a further sync re-reads nothing at all.
	callRetrieval(t, caller, `{"query":"stable","scope":"project_docs"}`)
	if mem.metaUpdates != 1 {
		t.Fatalf("probe refreshed again on an untouched file: metaUpdates=%d", mem.metaUpdates)
	}
}
