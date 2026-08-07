package agentprompt

import (
	"strings"
	"testing"

	toolcore "github.com/invokr-ai/invokr/tools/core"
)

// TestNativeToolsKeepRulesWithoutTheTaggedListing covers #446: under native
// tool calling the prompt carries the behavioural rules and the deferred-tool
// index, but not the tagged-JSON listing or its call examples — the tool
// definitions already ride the request's native tools array.
func TestNativeToolsKeepRulesWithoutTheTaggedListing(t *testing.T) {
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
		{Name: "lsp", Description: "A deferred tool."},
	}

	var out strings.Builder
	renderNativeTools(&out, specs)
	got := out.String()

	for _, want := range []string{
		"Deferred tools",
		"1. lsp: A deferred tool.",
		"Rules for tools:",
		"Before editing, obtain the current relevant span if it is not already in context or may have changed; do not reread unchanged content.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("native tool prompt missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Available tools:", "<|tool_call>", "read: Read a file."} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("native tool prompt still carries the tagged listing (%q):\n%s", unwanted, got)
		}
	}
}

// TestNativeToolRulesRenderWithoutDeferredTools pins that the rules survive
// when no tool is deferred (the old deferred-only path returned early).
func TestNativeToolRulesRenderWithoutDeferredTools(t *testing.T) {
	var out strings.Builder
	renderNativeTools(&out, []toolcore.ToolSpec{
		{Name: "read", Description: "Read a file.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}},
	})
	if got := out.String(); !strings.Contains(got, "Before editing, obtain the current relevant span if it is not already in context or may have changed; do not reread unchanged content.") {
		t.Fatalf("rules dropped without deferred tools:\n%s", got)
	}
}

// TestNativeToolsCarryUsageExamples covers the #446 follow-up: native mode
// keeps a compact arguments-only examples block (the tagged listing carried
// per-tool examples that anchored edit usage), with no tagged syntax.
func TestNativeToolsCarryUsageExamples(t *testing.T) {
	specs := []toolcore.ToolSpec{
		{Name: "edit", Description: "Edit a file.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}},
		{Name: "shell", Description: "Run a command.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}},
		{Name: "code_interpreter", Description: "Run Python.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}},
	}
	var out strings.Builder
	renderNativeTools(&out, specs)
	got := out.String()

	for _, want := range []string{
		"Tool usage examples (arguments only):",
		`- edit: {"path":"src/app.go","edits":[{"old":"","new":"package main\n"}]} (one edit with empty "old" creates the file)`,
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
