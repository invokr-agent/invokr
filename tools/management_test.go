package tools

import (
	"context"
	"encoding/json"
	"github.com/invokr-ai/invokr/capability/approval"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/tool_search"
	"go.uber.org/zap"
)

func managementCaller(t *testing.T, dir string) *Caller {
	t.Helper()
	c := &Caller{native: map[string]nativeEntry{}, logger: zap.NewNop(), app: core.CallerDeps{App: config.AppConfig{Memory: config.MemoryConfig{Dir: dir}}}}
	tool_search.RegisterManager(c, nil, c.manageTools, nil)
	for _, name := range []string{"read", "edit", "apply_patch", "optional"} {
		c.RegisterNative(core.ToolSpec{Name: name, Description: name + " description", Parameters: map[string]any{"type": "object"}}, func(context.Context, *core.ToolCall) (string, error) { return "executed", nil })
	}
	return c
}
func managerCall(t *testing.T, c *Caller, id, args string) (string, error) {
	t.Helper()
	return c.HandleCall(approved(core.WithSessionID(context.Background(), id)), &core.ToolCall{Name: "tools", Arguments: json.RawMessage(args)})
}
func TestManagerSessionStateAndAtomicChanges(t *testing.T) {
	dir := t.TempDir()
	c := managementCaller(t, dir)
	if err := c.StartToolSession("one"); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"action":"list"}`, `{"action":"search","query":"optional"}`} {
		out, err := managerCall(t, c, "one", args)
		if err != nil || !strings.Contains(out, `"enabled":false`) {
			t.Fatalf("discovery: %s %v", out, err)
		}
	}
	if _, err := managerCall(t, c, "one", `{"action":"enable","names":["optional","read"]}`); err == nil {
		t.Fatal("protected core accepted")
	}
	if len(c.managed["one"].Enabled) != 0 {
		t.Fatal("partially applied invalid enable batch")
	}
	out, err := managerCall(t, c, "one", `{"action":"enable","names":["optional"]}`)
	if err != nil || !strings.Contains(out, `"parameters":{"type":"object"}`) {
		t.Fatalf("enable: %s %v", out, err)
	}
	restored := managementCaller(t, dir)
	if err := restored.StartToolSession("one"); err != nil {
		t.Fatal(err)
	}
	if len(restored.managed["one"].Enabled) != 1 {
		t.Fatal("resume lost enabled state")
	}
	if err := restored.StartToolSession("two"); err != nil {
		t.Fatal(err)
	}
	if len(restored.managed["two"].Enabled) != 0 {
		t.Fatal("state leaked across sessions")
	}
	if _, err := managerCall(t, restored, "one", `{"action":"disable","names":["tools"]}`); err == nil {
		t.Fatal("manager disabled")
	}
}
func TestManagerRejectsDisabledAndDirectOptional(t *testing.T) {
	c := managementCaller(t, t.TempDir())
	if err := c.StartToolSession("one"); err != nil {
		t.Fatal(err)
	}
	ctx := core.WithSessionID(context.Background(), "one")
	call := &core.ToolCall{Name: "optional", Arguments: json.RawMessage(`{}`)}
	for _, managed := range []bool{false, true} {
		call.Managed = managed
		if _, err := c.HandleCall(ctx, call); err == nil {
			t.Fatal("disabled target ran")
		}
	}
	if _, err := managerCall(t, c, "one", `{"action":"enable","names":["optional"]}`); err != nil {
		t.Fatal(err)
	}
	call.Managed = false
	if _, err := c.HandleCall(ctx, call); err == nil {
		t.Fatal("direct optional call ran")
	}
	call.Managed = true
	if out, err := c.HandleCall(ctx, call); err != nil || out != "executed" {
		t.Fatalf("run: %q %v", out, err)
	}
	if _, err := managerCall(t, c, "one", `{"action":"disable","names":["optional"]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HandleCall(ctx, call); err == nil {
		t.Fatal("disabled target ran")
	}
}
func TestManagerHonorsOperatorRestrictions(t *testing.T) {
	c := managementCaller(t, t.TempDir())
	c.app.App.Tools.Allowlist = []string{"read"}
	if err := c.StartToolSession("one"); err != nil {
		t.Fatal(err)
	}
	out, err := managerCall(t, c, "one", `{"action":"list"}`)
	if err != nil || strings.Contains(out, `"optional"`) || !strings.Contains(out, `"tools"`) {
		t.Fatalf("restricted list: %s %v", out, err)
	}
	if _, err := managerCall(t, c, "one", `{"action":"enable","names":["optional"]}`); err == nil {
		t.Fatal("enabled forbidden tool")
	}
}

func TestManagerCreationScopeAndSchema(t *testing.T) {
	c := managementCaller(t, t.TempDir())
	ctx := context.WithValue(context.Background(), core.ToolScopeKey{}, map[string]struct{}{"tools": {}, "create_tool": {}})
	_, err := c.ResolveToolCall(ctx, &core.ToolCall{Name: "tools", Arguments: json.RawMessage(`{"action":"create","definition":{"name":"new_tool"}}`)})
	if err == nil {
		t.Fatal("out-of-scope creation must fail before writing")
	}
	for _, args := range []string{`{"action":"run","name":"tools","arguments":{}}`, `{"action":"run","name":"create_tool","arguments":{}}`, `{"action":"run","name":"optional","arguments":[]}`} {
		if _, err := c.ResolveToolCall(context.Background(), &core.ToolCall{Name: "tools", Arguments: json.RawMessage(args)}); err == nil {
			t.Fatalf("accepted invalid call: %s", args)
		}
	}
	var call core.ToolCall
	if err := json.Unmarshal([]byte(`{"name":"optional","arguments":{},"Managed":true}`), &call); err != nil {
		t.Fatal(err)
	}
	if call.Managed {
		t.Fatal("model forged trusted management flag")
	}
}

func TestManagerFreezesPromptCatalogAcrossChangesAndResume(t *testing.T) {
	dir := t.TempDir()
	c := managementCaller(t, dir)
	if err := c.StartToolSession("s"); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(c.PromptSpecs("s"))
	for _, args := range []string{`{"action":"enable","names":["optional"]}`, `{"action":"disable","names":["optional"]}`} {
		if _, err := managerCall(t, c, "s", args); err != nil {
			t.Fatal(err)
		}
	}
	c.RegisterNative(core.ToolSpec{Name: "late", Description: "Late plugin."}, func(context.Context, *core.ToolCall) (string, error) { return "", nil })
	after, _ := json.Marshal(c.PromptSpecs("s"))
	if string(before) != string(after) {
		t.Fatal("availability/registration rewrote prompt catalog")
	}
	listed, err := managerCall(t, c, "s", `{"action":"list"}`)
	if err != nil || !strings.Contains(listed, `"name":"late"`) {
		t.Fatal("late tool missing from live discovery")
	}
	restored := managementCaller(t, dir)
	if err := restored.StartToolSession("s"); err != nil {
		t.Fatal(err)
	}
	after, _ = json.Marshal(restored.PromptSpecs("s"))
	if string(before) != string(after) {
		t.Fatal("resume changed frozen catalog")
	}
	props := c.native["tools"].spec.Parameters["properties"].(map[string]any)
	if props["arguments"].(map[string]any)["type"] != "object" {
		t.Fatal("run arguments are not an object schema")
	}
}

func TestPatchIsProtectedCore(t *testing.T) {
	c := managementCaller(t, t.TempDir())
	if err := c.StartToolSession("s"); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"action":"enable","names":["apply_patch"]}`, `{"action":"disable","names":["apply_patch"]}`} {
		if _, err := managerCall(t, c, "s", args); err == nil {
			t.Fatal("manager changed protected patch availability")
		}
	}
	ctx := core.WithSessionID(context.Background(), "s")
	if out, err := c.HandleCall(ctx, &core.ToolCall{Name: "apply_patch", Arguments: json.RawMessage(`{}`)}); err != nil || out != "executed" {
		t.Fatalf("direct core call: %s %v", out, err)
	}
	c.app.App.Tools.Allowlist = []string{"read"}
	if _, err := c.HandleCall(ctx, &core.ToolCall{Name: "apply_patch", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("core bypassed operator restriction")
	}
}

func TestResumePromotedCoreOmitsOptionalCatalog(t *testing.T) {
	c := managementCaller(t, t.TempDir())
	state := sessionstore.ToolState{Enabled: []string{"apply_patch"}, Catalog: []sessionstore.ToolListing{{Name: "apply_patch", Description: "Old optional patch"}, {Name: "optional", Description: "Optional tool"}}}
	if err := c.toolStore().SaveToolState("old", state); err != nil {
		t.Fatal(err)
	}
	if err := c.StartToolSession("old"); err != nil {
		t.Fatal(err)
	}
	patchCount := 0
	for _, spec := range c.PromptSpecs("old") {
		if spec.Name == "apply_patch" {
			patchCount++
			if spec.Parameters == nil {
				t.Fatal("core patch remains optional")
			}
		}
	}
	if patchCount != 1 || len(c.managed["old"].Enabled) != 0 {
		t.Fatalf("resume patch count=%d enabled=%v", patchCount, c.managed["old"].Enabled)
	}
}
func TestManagerMisusedRunShowsRunShape(t *testing.T) {
	c := managementCaller(t, t.TempDir())
	if _, err := managerCall(t, c, "one", `{"action":"enable","names":["optional"]}`); err != nil {
		t.Fatal(err)
	}
	// Shapes a model sent in the M2 r177 baseline run after enabling tmux.
	for _, args := range []string{`{"action":"run","names":["optional"]}`, `{"action":"run","name":"optional"}`, `{"action":"read","name":"optional","arguments":{}}`} {
		if _, err := managerCall(t, c, "one", args); err == nil || !strings.Contains(err.Error(), tool_search.RunShape) {
			t.Fatalf("%s: error %v lacks the run shape", args, err)
		}
	}
}

func TestCoreToolPolicyIsCallerScoped(t *testing.T) {
	custom := managementCaller(t, t.TempDir())
	custom.app.App.Tools.Core = []string{"optional"}
	normal := managementCaller(t, t.TempDir())
	for _, tc := range []struct {
		name   string
		caller *Caller
		core   bool
	}{{"custom", custom, true}, {"default", normal, false}} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.caller
			specs := c.StableSpecs()
			if slices.ContainsFunc(specs, func(s core.ToolSpec) bool { return s.Name == "optional" }) != tc.core {
				t.Error("wrong stable schemas")
			}
			ctx := core.WithSessionID(context.Background(), "s")
			listed, err := c.ToolListings(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range listed {
				if item.Name == "optional" && item.Protected != tc.core {
					t.Error("wrong listing policy")
				}
			}
			_, err = c.HandleCall(ctx, &core.ToolCall{Name: "optional", Arguments: json.RawMessage(`{}`)})
			if (err == nil) != tc.core {
				t.Errorf("direct execution: %v", err)
			}
			_, err = c.ResolveToolCall(ctx, &core.ToolCall{Name: "tools", Arguments: json.RawMessage(`{"action":"run","name":"optional","arguments":{}}`)})
			if (err != nil) != tc.core {
				t.Errorf("manager routing: %v", err)
			}
			if err := c.SetToolEnabled(ctx, "s", "optional", false); (err != nil) != tc.core {
				t.Errorf("disable policy: %v", err)
			}
		})
	}
}

// approved runs a call as a YOLO session would, so "tools enable" is approved.
func approved(ctx context.Context) context.Context {
	ctx, _ = approval.NewPolicy(approval.YOLO, false).Begin(ctx, approval.Request{})
	return ctx
}

// TestManagerEnableAsksInManual: the model cannot widen its own toolset in a
// Manual session without the user's yes; Auto lets it through.
func TestManagerEnableAsksInManual(t *testing.T) {
	for mode, wantErr := range map[approval.Mode]bool{approval.Manual: true, approval.Auto: false} {
		c := managementCaller(t, t.TempDir())
		if err := c.StartToolSession("one"); err != nil {
			t.Fatal(err)
		}
		ctx, done := approval.NewPolicy(mode, false).Begin(core.WithSessionID(context.Background(), "one"), approval.Request{})
		_, err := c.HandleCall(ctx, &core.ToolCall{Name: "tools", Arguments: json.RawMessage(`{"action":"enable","names":["optional"]}`)})
		done()
		if (err != nil) != wantErr {
			t.Fatalf("%s: enable err = %v, want error %v", mode, err, wantErr)
		}
	}
}
