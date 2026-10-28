package agentprompt

import (
	"context"
	"strings"
	"testing"

	toolcore "github.com/invokr-agent/invokr/tools/core"
)

// TestNativeToolsOmitGenericGuidance keeps the deferred catalog in the bench
// variant while native descriptions continue to carry the tool contracts.
func TestNativeToolsOmitGenericGuidance(t *testing.T) {
	specs := []toolcore.ToolSpec{
		{
			Name: "read", Description: "Read a file.",
			Example:  `{"name":"read","arguments":{"path":"x"}}`,
			Metadata: toolcore.ToolMetadata{AlwaysVisible: true},
		},
		{
			Name: "todo", Description: "Track steps.",
			Metadata: toolcore.ToolMetadata{AlwaysVisible: true},
		},
		{Name: "tools"},
		{Name: "lsp", Description: "An optional tool."},
	}

	got := (&Builder{}).renderNativeTools(specs)

	for _, want := range []string{
		"Optional tools start disabled",
		"enable returns their schemas",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("native tool prompt missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"lsp: An optional tool.", "Available tools:", "<|tool_call>", "read: Read a file.", "Rules for tools:", todoRule, "Before editing,"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("native tool prompt still carries the tagged listing (%q):\n%s", unwanted, got)
		}
	}
}

func TestNativeToolsOmitGuidanceWithoutDeferredTools(t *testing.T) {
	got := (&Builder{}).renderNativeTools([]toolcore.ToolSpec{
		{Name: "read", Description: "Read a file.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}},
	})
	if got != "" {
		t.Fatalf("unexpected generic guidance: %q", got)
	}
}

// TestNativeToolsCarryUsageExamples covers the #446 follow-up: native mode
// keeps a compact arguments-only examples block (the tagged listing carried
// per-tool examples that anchored edit usage), with no tagged syntax.
func TestNativeToolsCarryUsageExamples(t *testing.T) {
	specs := []toolcore.ToolSpec{
		{Name: "edit", Description: "Edit or write a file.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}},
		{Name: "shell", Description: "Run a command.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}},
		{Name: "code_interpreter", Description: "Run Python.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}},
	}
	got := (&Builder{}).renderNativeTools(specs)

	for _, want := range []string{
		"Tool usage examples (arguments only):",
		`- edit: {"path":"src/app.go","content":"package main\n"} (creates or overwrites the file)`,
		`- shell: {"command":"go test ./... 2>&1 | tail -20","cwd":"."}`,
		`- code_interpreter: {"code":"open('artifacts/out.json','w').write('{}')"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("native prompt missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"<|tool_call>", `{"name":"edit","arguments"`, "Available tools:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("native prompt carries tagged syntax (%q):\n%s", unwanted, got)
		}
	}

	// The block is a fixed cost on every turn: keep it small.
	var bare strings.Builder
	renderNativeExamples(&bare, specs)
	if size := bare.Len(); size > 600 {
		t.Fatalf("examples block is %d chars, want <= 600", size)
	}
}

// TestNativeExamplesSkipAbsentTools keeps the block honest when a tool is not
// available in the session.
func TestNativeExamplesSkipAbsentTools(t *testing.T) {
	var out strings.Builder
	renderNativeExamples(&out, []toolcore.ToolSpec{{Name: "shell"}})
	got := out.String()
	if !strings.Contains(got, "- shell:") || strings.Contains(got, "edit") {
		t.Fatalf("examples = %q, want only the shell line", got)
	}
	var empty strings.Builder
	renderNativeExamples(&empty, []toolcore.ToolSpec{{Name: "read"}})
	if empty.Len() != 0 {
		t.Fatalf("examples rendered with no matching tool: %q", empty.String())
	}
}

func TestManagerTaggedSchemaAndNativeGuidance(t *testing.T) {
	specs := []toolcore.ToolSpec{
		{Name: "tools", Parameters: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string"}}}},
		{Name: "edit", Parameters: map[string]any{"type": "object"}},
		{Name: "unknown", Description: "Optional plugin."},
		{Name: "apply_patch", Description: "Patch core file.", Parameters: map[string]any{"type": "object"}},
	}
	for _, native := range []bool{false, true} {
		builder, err := NewBuilder(Config{NativeToolCalling: native}, testDeps(Deps{Tools: &fakeTools{specs: specs}}))
		if err != nil {
			t.Fatal(err)
		}
		prompt, err := builder.Build(context.Background(), Request{SessionID: "s1"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt.System, "Use tools to list") || strings.Contains(prompt.System, "Optional plugin.") {
			t.Fatalf("manager prompt mismatch: %s", prompt.System)
		}
		if native && strings.Contains(prompt.System, "Patch core file.") {
			t.Fatal("core patch leaked into optional catalog")
		}
		if !native && !strings.Contains(prompt.System, "Arguments schema:") {
			t.Fatal("tagged manager schema missing")
		}
	}
}
