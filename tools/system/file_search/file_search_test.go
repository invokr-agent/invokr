package file_search_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type searchResponse struct {
	Results []struct {
		Path      string  `json:"path"`
		LineStart int     `json:"line_start"`
		LineEnd   int     `json:"line_end"`
		Snippet   string  `json:"snippet"`
		Score     float64 `json:"score"`
		GitStatus string  `json:"git_status"`
	} `json:"results"`
}

func TestCallerRegistersFileSearchTool(t *testing.T) {
	caller := newFileSearchCaller(t, t.TempDir(), nil)

	if !slices.ContainsFunc(caller.Specs(), func(spec core.ToolSpec) bool { return spec.Name == "file_search" }) {
		t.Fatal("expected file_search tool to be registered")
	}
}

func TestFileSearchFilenameLiteralRegexAndGlob(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "tools", "caller.go"), "package tools\nfunc HandleCall() {}\n")
	mustWriteFile(t, filepath.Join(root, "notes.txt"), "HandleCall appears here\n")
	mustWriteFile(t, filepath.Join(root, "src", "nested", "target.go"), "package nested\nfunc HandleCallNested() {}\n")
	caller := newFileSearchCaller(t, root, nil)

	filename := callFileSearch(t, caller, `{"query":"caller","mode":"filename","max_results":5}`)
	assertPath(t, filename, "tools/caller.go")

	literal := callFileSearch(t, caller, `{"query":"HandleCall","mode":"literal","max_results":5}`)
	assertPath(t, literal, "tools/caller.go")

	regex := callFileSearch(t, caller, `{"query":"Handle[A-Z][a-z]+","mode":"regex","max_results":5}`)
	assertPath(t, regex, "tools/caller.go")

	globbed := callFileSearch(t, caller, `{"query":"HandleCall","mode":"literal","path_glob":"**/*.txt","max_results":5}`)
	if len(globbed.Results) != 1 || globbed.Results[0].Path != "notes.txt" {
		t.Fatalf("expected only notes.txt from glob, got %#v", globbed.Results)
	}

	recursiveGlob := callFileSearch(t, caller, `{"query":"HandleCallNested","mode":"literal","path_glob":"src/**/*.go","max_results":5}`)
	if len(recursiveGlob.Results) != 1 || recursiveGlob.Results[0].Path != "src/nested/target.go" {
		t.Fatalf("expected nested target.go from recursive glob, got %#v", recursiveGlob.Results)
	}
}

func TestFileSearchSkipsIgnoredHiddenAndBinaryFiles(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	mustWriteFile(t, filepath.Join(root, ".gitignore"), "ignored/\n*.log\n")
	mustWriteFile(t, filepath.Join(root, "sub", ".gitignore"), "generated.txt\n")
	mustWriteFile(t, filepath.Join(root, "sub", "deep", ".gitignore"), "ignored-deep/\n")
	mustWriteFile(t, filepath.Join(root, "visible.txt"), "needle\n")
	mustWriteFile(t, filepath.Join(root, "ignored", "skip.txt"), "needle\n")
	mustWriteFile(t, filepath.Join(root, "debug.log"), "needle\n")
	mustWriteFile(t, filepath.Join(root, "sub", "generated.txt"), "needle\n")
	mustWriteFile(t, filepath.Join(root, "sub", "deep", "keep.txt"), "needle\n")
	mustWriteFile(t, filepath.Join(root, "sub", "deep", "ignored-deep", "skip.txt"), "needle\n")
	mustWriteFile(t, filepath.Join(root, ".hidden.txt"), "needle\n")
	mustWriteBytes(t, filepath.Join(root, "binary.bin"), []byte{'n', 'e', 0, 'e'})
	caller := newFileSearchCaller(t, root, nil)

	resp := callFileSearch(t, caller, `{"query":"needle","mode":"literal","max_results":20}`)
	assertPath(t, resp, "visible.txt")
	assertPath(t, resp, "sub/deep/keep.txt")
	for _, path := range []string{"ignored/skip.txt", "debug.log", "sub/generated.txt", "sub/deep/ignored-deep/skip.txt", ".hidden.txt", "binary.bin"} {
		assertNotPath(t, resp, path)
	}
}

func TestFileSearchSkipsObviousBinaryContentButKeepsFilenameMatches(t *testing.T) {
	root := t.TempDir()
	mustWriteBytes(t, filepath.Join(root, "needle.png"), []byte("needle without a null byte"))
	caller := newFileSearchCaller(t, root, nil)

	literal := callFileSearch(t, caller, `{"query":"needle","mode":"literal"}`)
	assertNotPath(t, literal, "needle.png")
	filename := callFileSearch(t, caller, `{"query":"needle","mode":"filename"}`)
	assertPath(t, filename, "needle.png")
}

func TestFileSearchKeepsFilenameMatchWhenContentIsBinary(t *testing.T) {
	root := t.TempDir()
	mustWriteBytes(t, filepath.Join(root, "needle.dat"), []byte{'n', 'e', 0, 'e'})
	caller := newFileSearchCaller(t, root, nil)

	resp := callFileSearch(t, caller, `{"query":"needle","mode":"auto"}`)
	assertPath(t, resp, "needle.dat")
}

func TestFileSearchStreamsLongLines(t *testing.T) {
	root := t.TempDir()
	longLine := strings.Repeat("x", 128*1024) + " needle\n"
	mustWriteFile(t, filepath.Join(root, "large.txt"), longLine)
	caller := newFileSearchCaller(t, root, nil)

	resp := callFileSearch(t, caller, `{"query":"needle","mode":"literal","context_lines":0,"max_results":5}`)
	if len(resp.Results) != 1 || resp.Results[0].Path != "large.txt" || !strings.Contains(resp.Results[0].Snippet, "needle") {
		t.Fatalf("expected long line match, got %#v", resp.Results)
	}
}

func TestFileSearchRejectsOutsideWorkspaceGlob(t *testing.T) {
	root := t.TempDir()
	dispatcher := events.NewOrderedDispatcher(nil)
	var seen events.Event
	dispatcher.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventAudit {
			seen = ev
		}
		return nil
	}))
	caller := newFileSearchCaller(t, root, dispatcher)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "file_search",
		Arguments: json.RawMessage(`{"query":"x","path_glob":"../*.go"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "outside allowed workspace roots") {
		t.Fatalf("expected traversal glob rejection, got %v", err)
	}
	if seen.Audit == nil || seen.Audit.Outcome != "failed" {
		t.Fatalf("expected failed audit, got %#v", seen)
	}
}

func TestFileSearchSnippetsMaxResultsAndEmptyList(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "one\ntwo needle\nthree\n")
	mustWriteFile(t, filepath.Join(root, "b.txt"), "needle\n")
	caller := newFileSearchCaller(t, root, nil)

	resp := callFileSearch(t, caller, `{"query":"needle","mode":"literal","context_lines":1,"max_results":1}`)
	if len(resp.Results) != 1 {
		t.Fatalf("expected one capped result, got %#v", resp.Results)
	}
	if resp.Results[0].LineStart == 0 || !strings.Contains(resp.Results[0].Snippet, "2: two needle") {
		t.Fatalf("expected line-numbered snippet, got %#v", resp.Results[0])
	}

	noContext := callFileSearch(t, caller, `{"query":"two needle","mode":"literal","context_lines":0,"max_results":1}`)
	if len(noContext.Results) != 1 || noContext.Results[0].Snippet != "2: two needle" {
		t.Fatalf("expected explicit zero context lines to return only match line, got %#v", noContext.Results)
	}
	for _, limit := range []int{-1, 0, 1000} {
		resp := callFileSearch(t, caller, fmt.Sprintf(`{"query":"needle","mode":"literal","max_results":%d}`, limit))
		if len(resp.Results) != 2 {
			t.Fatalf("limit %d: expected both matches after normalization, got %#v", limit, resp.Results)
		}
	}

	empty, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "file_search", Arguments: json.RawMessage(`{"query":"absent","mode":"literal"}`)})
	if err != nil || empty != `{"results":[]}` {
		t.Fatalf("expected empty JSON array, got %q, error %v", empty, err)
	}
}

func TestFileSearchEmitsAuditEvent(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "needle\n")
	dispatcher := events.NewOrderedDispatcher(nil)
	var seen events.Event
	dispatcher.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventAudit {
			seen = ev
		}
		return nil
	}))
	caller := newFileSearchCaller(t, root, dispatcher)

	_ = callFileSearchWithContext(t, core.WithSessionID(context.Background(), "search-session"), caller, `{"query":"needle","mode":"literal","path_glob":"**/*.txt","git_scope":"all"}`)
	if seen.Type != events.EventAudit || seen.Audit == nil {
		t.Fatal("expected audit event")
	}
	if seen.SessionID != "search-session" {
		t.Fatalf("expected session id, got %q", seen.SessionID)
	}
	if seen.Audit.Tool != "file_search" || seen.Audit.Action != "file_search" || seen.Audit.Outcome != "completed" {
		t.Fatalf("expected completed file_search audit, got %#v", seen.Audit)
	}
	if seen.Audit.Details["policy_tier"] != "read_workspace" || seen.Audit.Details["query_hash"] == "" || seen.Audit.Details["result_count"] != 1 {
		t.Fatalf("expected file_search audit details, got %#v", seen.Audit.Details)
	}
}

func TestFileSearchGitScopes(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	mustWriteFile(t, filepath.Join(root, "tracked.txt"), "needle tracked\n")
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-m", "initial")
	mustWriteFile(t, filepath.Join(root, "tracked.txt"), "needle tracked changed\n")
	mustWriteFile(t, filepath.Join(root, "staged.txt"), "needle staged\n")
	runGit(t, root, "add", "staged.txt")
	mustWriteFile(t, filepath.Join(root, "untracked.txt"), "needle untracked\n")
	caller := newFileSearchCaller(t, root, nil)

	tracked := callFileSearch(t, caller, `{"query":"needle","mode":"literal","git_scope":"tracked","max_results":10}`)
	assertPath(t, tracked, "tracked.txt")
	assertNotPath(t, tracked, "untracked.txt")

	changed := callFileSearch(t, caller, `{"query":"needle","mode":"literal","git_scope":"changed","max_results":10}`)
	assertPath(t, changed, "tracked.txt")
	assertPath(t, changed, "staged.txt")
	assertPath(t, changed, "untracked.txt")

	staged := callFileSearch(t, caller, `{"query":"needle","mode":"literal","git_scope":"staged","max_results":10}`)
	assertPath(t, staged, "staged.txt")
	assertNotPath(t, staged, "untracked.txt")

	unstaged := callFileSearch(t, caller, `{"query":"needle","mode":"literal","git_scope":"unstaged","max_results":10}`)
	assertPath(t, unstaged, "tracked.txt")
	assertPath(t, unstaged, "untracked.txt")
	assertNotPath(t, unstaged, "staged.txt")
}

func TestFileSearchWorksInNonGitWorkspace(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "plain.txt"), "needle in plain workspace\n")
	mustWriteFile(t, filepath.Join(root, "nested", "deep.txt"), "needle in nested file\n")
	caller := newFileSearchCaller(t, root, nil)

	resp := callFileSearch(t, caller, `{"query":"needle","mode":"literal","max_results":10}`)
	assertPath(t, resp, "plain.txt")
	assertPath(t, resp, "nested/deep.txt")
}

func TestFileSearchGitScopeRequiresGitRepo(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "needle\n")
	caller := newFileSearchCaller(t, root, nil)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "file_search",
		Arguments: json.RawMessage(`{"query":"needle","git_scope":"tracked"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "git metadata unavailable") {
		t.Fatalf("expected git metadata error, got %v", err)
	}
}

func TestFileSearchAllowsHiddenFilesWithWorkspacePolicy(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".hidden.txt"), "needle\n")
	caller := newFileSearchCaller(t, root, func(cfg *config.AppConfig) {
		cfg.Workspace.AllowHiddenPaths = true
	})

	resp := callFileSearch(t, caller, `{"query":"needle","mode":"literal"}`)
	assertPath(t, resp, ".hidden.txt")
}

func newFileSearchCaller(t *testing.T, root string, customize any) *tools.Caller {
	t.Helper()
	app := config.AppConfig{
		Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
		Workspace: config.WorkspaceConfig{Root: root},
	}
	var dispatcher events.Dispatcher
	switch v := customize.(type) {
	case nil:
	case events.Dispatcher:
		dispatcher = v
	case func(*config.AppConfig):
		v(&app)
	default:
		t.Fatalf("unsupported customize type %T", customize)
	}
	return tools.NewCaller(core.CallerDeps{App: app, Dispatcher: dispatcher})
}

func callFileSearch(t *testing.T, caller *tools.Caller, args string) searchResponse {
	t.Helper()
	return callFileSearchWithContext(t, context.Background(), caller, args)
}

func callFileSearchWithContext(t *testing.T, ctx context.Context, caller *tools.Caller, args string) searchResponse {
	t.Helper()
	output, err := caller.HandleCall(ctx, &core.ToolCall{
		Name:      "file_search",
		Arguments: json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("file_search failed: %v", err)
	}
	var resp searchResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("decode file_search output %q: %v", output, err)
	}
	return resp
}

func assertPath(t *testing.T, resp searchResponse, path string) {
	t.Helper()
	for _, result := range resp.Results {
		if result.Path == path {
			return
		}
	}
	t.Fatalf("expected path %q in results %#v", path, resp.Results)
}

func assertNotPath(t *testing.T, resp searchResponse, path string) {
	t.Helper()
	for _, result := range resp.Results {
		if result.Path == path {
			t.Fatalf("did not expect path %q in results %#v", path, resp.Results)
		}
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	mustWriteBytes(t, path, []byte(content))
}

func mustWriteBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func initGitRepo(t *testing.T, root string) {
	t.Helper()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test User")
}

// A repository-provided symlink named like an allowed file must not read
// through to a target the policy would refuse (PR #585 review).
func TestFileSearchResolvesSymlinkCandidatesBeforeReading(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ws")
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "secret")
	for path, content := range map[string]string{outside: "needle outside\n", filepath.Join(root, ".hidden", "inner"): "needle hidden\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".hidden", "inner"), filepath.Join(root, "sub.gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("needle plain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := callFileSearch(t, newFileSearchCaller(t, root, nil), `{"query":"needle","mode":"literal"}`)
	out, _ := json.Marshal(resp)
	if strings.Contains(string(out), "outside") || strings.Contains(string(out), "hidden") || !strings.Contains(string(out), "notes.txt") {
		t.Fatalf("symlinked candidates leaked or plain file missing: %s", out)
	}
}

type approverFunc func(context.Context, approval.Request) (approval.Decision, error)

func (f approverFunc) RequestApproval(ctx context.Context, req approval.Request) (approval.Decision, error) {
	return f(ctx, req)
}

// An outside dir is searched after one approval for that dir; results carry
// absolute paths so a follow-up read does not resolve them inside the workspace,
// and hidden files there stay out.
func TestFileSearchOutsideDirAsksOnce(t *testing.T) {
	other := t.TempDir()
	mustWriteFile(t, filepath.Join(other, "notes", "spec.md"), "needle here\n")
	mustWriteFile(t, filepath.Join(other, ".secret", "key.md"), "needle hidden\n")
	caller := newFileSearchCaller(t, t.TempDir(), nil)
	var asked []approval.Request
	p := approval.NewPolicy(approval.Auto, false)
	p.SetOutsideReads(true)
	ctx, done := p.Begin(context.Background(), approval.Request{Tool: "file_search"}, approverFunc(func(_ context.Context, req approval.Request) (approval.Decision, error) {
		if req.Outside {
			asked = append(asked, req)
		}
		return approval.Decision{Approved: true}, nil
	}))
	defer done()

	out, err := caller.HandleCall(ctx, &core.ToolCall{Name: "file_search", Arguments: json.RawMessage(fmt.Sprintf(`{"query":"needle","mode":"literal","dir":%q}`, other))})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var resp searchResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(asked) != 1 || asked[0].GrantPattern != other+"/*" {
		t.Fatalf("prompts = %+v, want one granting %s/*", asked, other)
	}
	if len(resp.Results) != 1 || resp.Results[0].Path != filepath.Join(other, "notes", "spec.md") {
		t.Fatalf("results = %+v, want only the absolute non-hidden match", resp.Results)
	}
}
