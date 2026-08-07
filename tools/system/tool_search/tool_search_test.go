package tool_search_test

import (
	"context"
	"encoding/json"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type searchResponse struct {
	Tools []struct {
		Name         string                `json:"name"`
		Namespace    string                `json:"namespace"`
		Description  string                `json:"description"`
		Parameters   map[string]any        `json:"parameters,omitempty"`
		Source       string                `json:"source"`
		Runtime      string                `json:"runtime,omitempty"`
		PluginDir    string                `json:"plugin_dir,omitempty"`
		Policy       core.ToolPolicy       `json:"policy"`
		Capabilities core.ToolCapabilities `json:"capabilities"`
	} `json:"tools"`
	Note string `json:"note"`
}

func TestToolSearchRegisteredAndAlwaysVisible(t *testing.T) {
	caller := newToolSearchCaller(t, t.TempDir(), nil)
	specs := caller.PromptSpecs("session-1")

	if !slices.ContainsFunc(specs, func(spec core.ToolSpec) bool { return spec.Name == "tool_search" && spec.Parameters != nil }) {
		t.Fatalf("expected tool_search schema visible, got %#v", specs)
	}
	if !slices.ContainsFunc(caller.CallableSpecs("session-1"), func(spec core.ToolSpec) bool { return spec.Name == "tool_search" }) {
		t.Fatal("expected tool_search callable at startup")
	}
}

func TestDeferredToolNameVisibleButSchemaHidden(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, root, "alpha_tool", "Alpha toolbox tool", map[string]string{
		"main.py": `print('{"result":"alpha"}')`,
	})
	caller := newToolSearchCaller(t, root, nil)

	specs := caller.PromptSpecs("session-1")
	var alpha core.ToolSpec
	for _, spec := range specs {
		if spec.Name == "alpha_tool" {
			alpha = spec
			break
		}
	}
	if alpha.Name == "" || alpha.Description == "" {
		t.Fatalf("expected alpha name and description visible, got %#v", alpha)
	}
	if alpha.Example != "" || alpha.Parameters != nil {
		t.Fatalf("expected deferred schema hidden, got %#v", alpha)
	}
	if slices.ContainsFunc(caller.CallableSpecs("session-1"), func(spec core.ToolSpec) bool { return spec.Name == "alpha_tool" }) {
		t.Fatal("did not expect alpha callable before schema load")
	}
}

func TestToolSearchReturnsSchemaAndUnlocksDeferredTool(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, root, "alpha_tool", "Alpha toolbox tool", map[string]string{
		"main.py": `print('{"result":"alpha"}')`,
	})
	caller := newToolSearchCaller(t, root, nil)
	ctx := core.WithSessionID(context.Background(), "session-1")

	resp := callToolSearch(t, ctx, caller, `{"query":"alpha","include_schema":true}`)
	if len(resp.Tools) != 1 || resp.Tools[0].Name != "alpha_tool" || resp.Tools[0].Parameters == nil {
		t.Fatalf("expected alpha schema result, got %#v", resp.Tools)
	}
	if !slices.ContainsFunc(caller.CallableSpecs("session-1"), func(spec core.ToolSpec) bool { return spec.Name == "alpha_tool" }) {
		t.Fatal("expected alpha callable after schema load")
	}
	output, err := caller.HandleCall(ctx, &core.ToolCall{Name: "alpha_tool", Arguments: json.RawMessage(`{}`)})
	if err != nil || output != "alpha" {
		t.Fatalf("expected alpha call success, got output=%q err=%v", output, err)
	}
}

func TestToolSearchWithoutIncludeSchemaAutoLoadsNarrowResult(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, root, "alpha_tool", "Alpha toolbox tool", map[string]string{
		"main.py": `print('{"result":"alpha"}')`,
	})
	caller := newToolSearchCaller(t, root, nil)
	ctx := core.WithSessionID(context.Background(), "session-1")

	resp := callToolSearch(t, ctx, caller, `{"query":"alpha"}`)
	if len(resp.Tools) != 1 || resp.Tools[0].Name != "alpha_tool" {
		t.Fatalf("expected alpha hit, got %#v", resp.Tools)
	}
	if !strings.Contains(resp.Note, "schema loaded") {
		t.Fatalf("expected schema-loaded note, got %q", resp.Note)
	}
	if resp.Tools[0].Parameters == nil {
		t.Fatal("expected the auto-loaded schema in the result")
	}
	if !slices.ContainsFunc(caller.CallableSpecs("session-1"), func(spec core.ToolSpec) bool { return spec.Name == "alpha_tool" }) {
		t.Fatal("expected alpha callable after search without include_schema")
	}
}

func TestToolSearchWideResultSaysSchemasNotLoaded(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"wide_one", "wide_two", "wide_three", "wide_four"} {
		testutil.MustWriteToolboxPlugin(t, root, name, "widget fixture tool", map[string]string{
			"main.py": `print('{"result":"wide"}')`,
		})
	}
	caller := newToolSearchCaller(t, root, nil)
	resp := callToolSearch(t, core.WithSessionID(context.Background(), "session-1"), caller, `{"query":"widget"}`)
	if len(resp.Tools) != 4 {
		t.Fatalf("expected 4 widget hits, got %#v", resp.Tools)
	}
	if !strings.Contains(resp.Note, "include_schema") {
		t.Fatalf("expected not-loaded note, got %q", resp.Note)
	}
	if resp.Tools[0].Parameters != nil {
		t.Fatalf("expected parameters omitted on a wide search, got %#v", resp.Tools[0].Parameters)
	}
	if slices.ContainsFunc(caller.CallableSpecs("session-1"), func(spec core.ToolSpec) bool { return spec.Name == "wide_one" }) {
		t.Fatal("did not expect a wide search to unlock schemas")
	}
}

func TestToolSearchMatchesMultipleTermsAcrossMetadata(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, root, "alpha_tool", "Echo fixture text", map[string]string{
		"main.py": `print('{"result":"alpha"}')`,
	})
	caller := newToolSearchCaller(t, root, nil)
	resp := callToolSearch(t, core.WithSessionID(context.Background(), "session-1"), caller, `{"query":"alpha echo fixture","include_schema":true}`)
	if len(resp.Tools) != 1 || resp.Tools[0].Name != "alpha_tool" {
		t.Fatalf("expected multi-term match, got %#v", resp.Tools)
	}
}

func TestForgetSessionClearsLoadedSchemas(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, root, "alpha_tool", "Alpha toolbox tool", map[string]string{
		"main.py": `print('{"result":"alpha"}')`,
	})
	caller := newToolSearchCaller(t, root, nil)

	caller.MarkSchemasLoaded("session-1", []string{"alpha_tool"})
	caller.MarkSchemasLoaded("session-2", []string{"alpha_tool"})
	if !slices.ContainsFunc(caller.CallableSpecs("session-1"), func(spec core.ToolSpec) bool { return spec.Name == "alpha_tool" }) {
		t.Fatal("expected schema unlock before ForgetSession")
	}

	revision := caller.Revision()
	caller.ForgetSession("session-1")

	if slices.ContainsFunc(caller.CallableSpecs("session-1"), func(spec core.ToolSpec) bool { return spec.Name == "alpha_tool" }) {
		t.Fatal("expected schema unlock cleared after ForgetSession")
	}
	if !slices.ContainsFunc(caller.CallableSpecs("session-2"), func(spec core.ToolSpec) bool { return spec.Name == "alpha_tool" }) {
		t.Fatal("expected other session's schema unlock to remain")
	}
	if caller.Revision() == revision {
		t.Fatal("expected ForgetSession to bump the revision")
	}
	// Idempotent: forgetting an unknown/already-forgotten session is a no-op.
	revision = caller.Revision()
	caller.ForgetSession("session-1")
	caller.ForgetSession("never-seen")
	if caller.Revision() != revision {
		t.Fatal("expected repeat ForgetSession to be a no-op")
	}
}

func TestToolSearchMetadataRefreshAndAudit(t *testing.T) {
	root := t.TempDir()
	dispatcher := events.NewOrderedDispatcher(nil)
	var audit events.Event
	refreshEvents := 0
	dispatcher.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventAudit {
			audit = ev
		}
		if ev.Type == events.EventToolboxRefreshRequested {
			refreshEvents++
		}
		return nil
	}))
	caller := newToolSearchCaller(t, root, dispatcher)

	testutil.MustWriteToolboxPlugin(t, root, "fresh_tool", "Fresh toolbox tool", map[string]string{
		"main.py": `print('{"result":"fresh"}')`,
	})
	ctx := core.WithSessionID(context.Background(), "refresh-session")
	_ = callToolSearch(t, ctx, caller, `{"namespace":"toolbox","include_schema":false}`)
	resp := waitForToolSearchResult(t, ctx, caller, "fresh_tool")
	if len(resp.Tools) != 1 || resp.Tools[0].Name != "fresh_tool" {
		t.Fatalf("expected refreshed fresh_tool, got %#v", resp.Tools)
	}
	tool := resp.Tools[0]
	if tool.Source != "toolbox" || tool.Runtime != "python" || !strings.Contains(filepath.ToSlash(tool.PluginDir), "fresh_tool") {
		t.Fatalf("expected toolbox source/runtime/plugin dir metadata, got %#v", tool)
	}
	if len(tool.Policy.Tiers) != 1 || tool.Policy.Tiers[0] != "read_workspace" || tool.Policy.Approval != constants.ApprovalPolicyAuto || !tool.Capabilities.ExecutesCode {
		t.Fatalf("expected policy and capabilities metadata, got %#v", tool)
	}
	// A single hit auto-loads (#534), so the schema comes back even though the
	// call set include_schema:false; the audit still records what was asked for.
	if tool.Parameters == nil {
		t.Fatal("expected the auto-loaded schema on a single hit")
	}
	if audit.Audit == nil || audit.SessionID != "refresh-session" || audit.Audit.Tool != "tool_search" || audit.Audit.Action != "tool_search" {
		t.Fatalf("expected tool_search audit, got %#v", audit)
	}
	if audit.Audit.Details["policy_tier"] != "read_workspace" || audit.Audit.Details["result_count"] != 1 || audit.Audit.Details["include_schema"] != false {
		t.Fatalf("expected audit details, got %#v", audit.Audit.Details)
	}
	if refreshEvents == 0 {
		t.Fatal("expected tool_search to request toolbox refresh event")
	}
}

func TestToolSearchDebouncesUnchangedToolboxRefresh(t *testing.T) {
	root := t.TempDir()
	dispatcher := events.NewOrderedDispatcher(nil)
	caller := newToolSearchCaller(t, root, dispatcher)
	ctx := core.WithSessionID(context.Background(), "debounce-session")

	_ = callToolSearch(t, ctx, caller, `{"namespace":"toolbox","include_schema":false}`)
	revision := caller.Revision()
	_ = callToolSearch(t, ctx, caller, `{"namespace":"toolbox","include_schema":false}`)
	if caller.Revision() != revision {
		t.Fatalf("unchanged toolbox refresh changed revision: %d -> %d", revision, caller.Revision())
	}
}

func newToolSearchCaller(t *testing.T, toolboxDir string, dispatcher events.Dispatcher) *tools.Caller {
	t.Helper()
	return tools.NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox:   config.ToolboxConfig{Dir: toolboxDir, AllowUnsandboxed: true},
		Workspace: config.WorkspaceConfig{Root: t.TempDir()},
	}, Dispatcher: dispatcher})
}

func callToolSearch(t *testing.T, ctx context.Context, caller *tools.Caller, args string) searchResponse {
	t.Helper()
	output, err := caller.HandleCall(ctx, &core.ToolCall{Name: "tool_search", Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("tool_search failed: %v", err)
	}
	var resp searchResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("decode tool_search output %q: %v", output, err)
	}
	return resp
}

func waitForToolSearchResult(t *testing.T, ctx context.Context, caller *tools.Caller, name string) searchResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := callToolSearch(t, ctx, caller, `{"namespace":"toolbox","include_schema":false}`)
		if len(resp.Tools) == 1 && resp.Tools[0].Name == name {
			return resp
		}
		time.Sleep(10 * time.Millisecond)
	}
	return callToolSearch(t, ctx, caller, `{"namespace":"toolbox","include_schema":false}`)
}
