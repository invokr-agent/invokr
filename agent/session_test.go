package agent

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"go.uber.org/zap"
)

func TestProviderCreateToolSchemaRequiresCapabilities(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{
		Toolbox: config.ToolboxConfig{Dir: t.TempDir()}, Workspace: config.WorkspaceConfig{Root: t.TempDir()},
	}}, zap.NewNop())
	defs := (OpenAINativeToolCodec{}).NativeToolDefinitions(caller.Specs())
	for _, def := range defs {
		if def.Name != "create_tool" {
			continue
		}
		wire, err := json.Marshal(def)
		if err != nil {
			t.Fatal(err)
		}
		var exposed struct {
			Parameters struct {
				Required []string `json:"required"`
			} `json:"parameters"`
		}
		if err := json.Unmarshal(wire, &exposed); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(exposed.Parameters.Required, "capabilities") {
			t.Fatalf("provider create_tool required = %v, want capabilities", exposed.Parameters.Required)
		}
		return
	}
	t.Fatal("provider does not expose create_tool")
}

func TestNativeToolDefinitionsUseSpecParameters(t *testing.T) {
	defs := (OpenAINativeToolCodec{}).NativeToolDefinitions([]core.ToolSpec{{
		Name:        "memory",
		Description: "Manage memory.",
		Example:     `{"name":"memory","arguments":{"action":"list"}}`,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []string{"save", "list"},
				},
			},
		},
	}})

	if len(defs) != 1 {
		t.Fatalf("native tool definitions = %d, want 1", len(defs))
	}
	if got := defs[0].Description; got != "Manage memory.\nExample: {\"name\":\"memory\",\"arguments\":{\"action\":\"list\"}}" {
		t.Fatalf("description = %q", got)
	}
	params := defs[0].Parameters
	if params["additionalProperties"] != false {
		t.Fatalf("parameters did not preserve additionalProperties: %#v", params)
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing properties: %#v", params)
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing action property: %#v", properties)
	}
	if action["type"] != "string" {
		t.Fatalf("action schema = %#v, want string type", action)
	}
}

func TestNewSessionReturnsErrorForMissingDeps(t *testing.T) {
	_, err := NewSession(SessionDeps{}, config.AppConfig{})
	if err == nil {
		t.Fatal("NewSession() error = nil, want missing dependency error")
	}
	if got, want := err.Error(), "agent: logger dependency is nil"; got != want {
		t.Fatalf("NewSession() error = %q, want %q", got, want)
	}
}

// A deferred tool (no schema loaded) is listed in the prompt for tool_search;
// shipping it as a native definition with an open schema invites a call that
// can only fail.
func TestNativeToolDefinitionsSkipDeferredSpecs(t *testing.T) {
	defs := (OpenAINativeToolCodec{}).NativeToolDefinitions([]core.ToolSpec{
		{Name: "apply_patch", Description: "deferred"},
		{Name: "edit", Description: "loaded", Parameters: map[string]any{"type": "object"}},
		{Name: "tool_search", Description: "always", Metadata: core.ToolMetadata{AlwaysVisible: true}},
	})
	if len(defs) != 2 || defs[0].Name != "edit" || defs[1].Name != "tool_search" {
		t.Fatalf("defs = %+v, want edit and tool_search only", defs)
	}
}
