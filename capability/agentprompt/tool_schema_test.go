package agentprompt

import (
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/constants"
	toolcore "github.com/invokr-agent/invokr/tools/core"
)

// The tagged-JSON tool listing has no separate function-definition channel, so
// a toolbox manifest's `parameters` has to be inlined for the model to see it.
// That inlining is scoped to toolbox tools on purpose: every native tool also
// carries a schema, and rendering those would grow the system prompt with the
// tool set for no gain. This pins both halves of that rule.
func TestRenderToolsInlinesOnlyToolboxSchemas(t *testing.T) {
	pluginSchema := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}
	specs := []toolcore.ToolSpec{{
		Name:        "plugin_tool",
		Description: "A manifest-declared tool.",
		Parameters:  pluginSchema,
		Metadata:    toolcore.ToolMetadata{Source: constants.ToolboxSource},
	}, {
		Name:        "native_tool",
		Description: "A compiled-in tool.",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"native_only_property": map[string]any{"type": "string"}}},
		Metadata:    toolcore.ToolMetadata{Source: "native"},
	}}

	got := renderTools(specs, syntax.NewTaggedJSONSyntax())

	want := "1. plugin_tool: A manifest-declared tool.\n" +
		`   Arguments schema: {"properties":{"path":{"type":"string"}},"type":"object"}` + "\n" +
		"2. native_tool: A compiled-in tool.\n"
	if !strings.Contains(got, want) {
		t.Fatalf("rendered tools =\n%s\nwant it to contain:\n%s", got, want)
	}
	if strings.Contains(got, "native_only_property") {
		t.Fatalf("a native tool's schema was inlined into the system prompt:\n%s", got)
	}
}
