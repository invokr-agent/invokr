package tools

import (
	"context"
	"encoding/json"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/tools/core"
	"slices"
	"testing"

	"go.uber.org/zap"
)

func TestSystemToolMetadata(t *testing.T) {
	// web_search registers only with a usable provider.
	t.Setenv(constants.BraveSearchAPIKeyEnv, "key")
	caller := NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: t.TempDir()},
			WebSearch: config.WebSearchConfig{Providers: []string{constants.WebSearchProviderBrave}},
		},
	}, zap.NewNop())

	if _, ok := findToolSpec(caller.Specs(), "edit_file"); ok {
		t.Fatal("legacy edit_file tool remains registered")
	}
	if _, ok := findToolSpec(caller.Specs(), "write"); ok {
		t.Fatal("separate write tool remains registered")
	}
	assertToolMetadata(t, caller, "edit", "workspace", []string{"write_workspace"}, constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true})
	assertToolMetadata(t, caller, "file_search", "workspace", []string{"read_workspace"}, constants.ApprovalPolicyAuto, core.ToolCapabilities{ReadsWorkspace: true})
	assertToolMetadata(t, caller, constants.WebSearchToolName, "network", []string{constants.WebSearchPolicyTier}, constants.ApprovalPolicyAsk, core.ToolCapabilities{Network: true})
	assertToolMetadata(t, caller, constants.ShellToolName, "system", []string{constants.ShellTierReadonly, constants.ShellTierWrite, constants.ShellTierNetwork}, constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true, Network: true, ExecutesCode: true})

	// apply_patch is protected core while keeping its existing approval policy.
	spec, ok := findToolSpec(caller.Specs(), "apply_patch")
	if !ok {
		t.Fatal("expected apply_patch registered")
	}
	if _, eager := findToolSpec(caller.StableSpecs(), spec.Name); !eager {
		t.Fatal("apply_patch missing from native core")
	}
}

func TestApplyPatchAdvertisedViaAllowlist(t *testing.T) {
	caller := NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: t.TempDir()},
			Tools:     config.ToolsConfig{Allowlist: []string{"apply_patch"}},
		},
	}, zap.NewNop())

	spec, ok := findToolSpec(caller.Specs(), "apply_patch")
	if !ok {
		t.Fatal("expected apply_patch registered")
	}
	if _, allowed := findToolSpec(caller.AvailableSpecs(), spec.Name); !allowed {
		t.Fatal("allowlisted apply_patch missing from discovery")
	}
}

func TestTodoDisabledRemovesTool(t *testing.T) {
	caller := NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		Tools:   config.ToolsConfig{TodoDisabled: true},
	}}, zap.NewNop())
	if caller.HasNative("todo") {
		t.Fatal("todo tool registered while disabled")
	}
}

func TestReplaceNativeToolsBySourceRemovesStaleTools(t *testing.T) {
	caller := NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
		Workspace: config.WorkspaceConfig{Root: t.TempDir()},
	}}, zap.NewNop())

	caller.ReplaceNativeToolsBySource("toolbox", []core.DynamicNativeTool{{
		Spec: core.ToolSpec{Name: "alpha", Description: "alpha", Example: `{}`, Metadata: core.ToolMetadata{Source: "toolbox", Namespace: "toolbox"}},
		Handler: func(context.Context, *core.ToolCall) (string, error) {
			return "alpha", nil
		},
	}})
	if _, ok := findToolSpec(caller.Specs(), "alpha"); !ok {
		t.Fatal("expected alpha registered")
	}

	caller.ReplaceNativeToolsBySource("toolbox", nil)
	if _, ok := findToolSpec(caller.Specs(), "alpha"); ok {
		t.Fatal("expected stale toolbox tool removed")
	}
	if _, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "alpha", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("expected removed toolbox tool call to fail")
	}
}

func TestReplaceNativeToolsBySourceDoesNotOverwriteNativeTool(t *testing.T) {
	caller := NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
		Workspace: config.WorkspaceConfig{Root: t.TempDir()},
	}}, zap.NewNop())

	caller.RegisterNative(core.ToolSpec{Name: "owned", Description: "native", Example: `{}`}, func(context.Context, *core.ToolCall) (string, error) {
		return "native", nil
	})
	skipped := caller.ReplaceNativeToolsBySource("toolbox", []core.DynamicNativeTool{{
		Spec: core.ToolSpec{Name: "owned", Description: "toolbox", Example: `{}`, Metadata: core.ToolMetadata{Source: "toolbox", Namespace: "toolbox"}},
		Handler: func(context.Context, *core.ToolCall) (string, error) {
			return "toolbox", nil
		},
	}})
	if !slices.Equal(skipped, []string{"owned"}) {
		t.Fatalf("expected owned skipped, got %#v", skipped)
	}
	got, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "owned", Arguments: json.RawMessage(`{}`)})
	if err != nil || got != "native" {
		t.Fatalf("expected native handler to remain, got output=%q err=%v", got, err)
	}
	spec, ok := findToolSpec(caller.Specs(), "owned")
	if !ok || spec.Metadata.Source != "native" {
		t.Fatalf("expected native spec to remain, got %#v", spec)
	}
}

func assertToolMetadata(t *testing.T, caller *Caller, name string, namespace string, tiers []string, approval string, capabilities core.ToolCapabilities) {
	t.Helper()
	spec, ok := findToolSpec(caller.Specs(), name)
	if !ok {
		t.Fatalf("tool %q not registered", name)
	}
	meta := spec.Metadata
	if meta.Namespace != namespace || meta.Source != "native" {
		t.Fatalf("%s metadata = %#v, want namespace %q source native", name, meta, namespace)
	}
	if !slices.Equal(meta.Policy.Tiers, tiers) || meta.Policy.Approval != approval {
		t.Fatalf("%s policy = %#v, want tiers %#v approval %q", name, meta.Policy, tiers, approval)
	}
	if meta.Capabilities != capabilities {
		t.Fatalf("%s capabilities = %#v, want %#v", name, meta.Capabilities, capabilities)
	}
}

func findToolSpec(specs []core.ToolSpec, name string) (core.ToolSpec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return core.ToolSpec{}, false
}
