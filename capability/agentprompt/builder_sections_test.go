package agentprompt

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/skills"
	toolcore "github.com/invokr-ai/invokr/tools/core"
)

func TestBuildSectionsSumToSystemLength(t *testing.T) {
	builder, err := NewBuilder(Config{BasePrompt: "Base prompt."}, testDeps(Deps{
		Memory: &fakeMemory{blocks: core.PromptBlocks{{Title: "Project Memory", Content: "Use tokf."}}},
		Skills: &fakeSkills{specs: []skills.Spec{{Name: "brief", Description: "Keep answers short."}}},
		Tools:  &fakeTools{specs: []toolcore.ToolSpec{{Name: "shell", Description: "Run a command."}}},
	}))
	if err != nil {
		t.Fatalf("NewBuilder() error = %v", err)
	}
	prompt, err := builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hello"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(prompt.Sections) == 0 {
		t.Fatalf("Build() returned no sections")
	}
	total := 0
	names := map[string]bool{}
	for _, section := range prompt.Sections {
		if section.Chars <= 0 {
			t.Fatalf("section %q has non-positive size %d", section.Name, section.Chars)
		}
		total += section.Chars
		names[section.Name] = true
	}
	if total != len(prompt.System) {
		t.Fatalf("sections sum to %d, system prompt is %d chars", total, len(prompt.System))
	}
	// Recalled memory is no longer a system-prompt section: it rides the user
	// message so the cached prefix stays stable (issue #359).
	if names["memory"] {
		t.Fatalf("memory must not be a system-prompt section: %v", prompt.Sections)
	}
	for _, want := range []string{"persona", "working rules", "response contract", "skills", "tool specs"} {
		if !names[want] {
			t.Fatalf("missing section %q in %v", want, prompt.Sections)
		}
	}
}

// The working rules sit directly after the base prompt, before the tool
// contract, even when only read tools are available.
func TestBuildRendersWorkRulesRightAfterBasePrompt(t *testing.T) {
	shell := toolcore.ToolSpec{Name: "shell", Description: "run", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}}
	builder, err := NewBuilder(Config{BasePrompt: "Base prompt.", NativeToolCalling: true}, testDeps(Deps{Tools: &fakeTools{specs: []toolcore.ToolSpec{shell}}}))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prompt.System, "Base prompt.\n\nWorking rules:\n") ||
		!strings.Contains(prompt.System, "A reply with no tool call ends the task.") {
		t.Fatalf("system prompt = %q", prompt.System)
	}
	if strings.Contains(prompt.System, "Rules for tools:") || strings.Contains(prompt.System, "shell runs one full bash line") {
		t.Fatalf("generic tool guidance remains in prompt:\n%s", prompt.System)
	}

	readOnly, _ := NewBuilder(Config{BasePrompt: "Base prompt."}, testDeps(Deps{Tools: &fakeTools{specs: []toolcore.ToolSpec{{Name: "read", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}}}}}))
	prompt, _ = readOnly.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hi"})
	if !strings.HasPrefix(prompt.System, "Base prompt.\n\nWorking rules:\n") {
		t.Fatalf("work rules missing for read-only tools:\n%s", prompt.System)
	}
}

// A usage rule for apply_patch is only right when the model can call it; a
// deferred apply_patch yields the edit rule instead.
func TestToolRulesRouteEditsToEditFileWhenApplyPatchIsDeferred(t *testing.T) {
	rules := strings.Join(toolRules([]toolcore.ToolSpec{{Name: "apply_patch"}, {Name: "edit", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}}}), "\n")
	if strings.Contains(rules, "Use apply_patch") || !strings.Contains(rules, "Use edit with an `edits` list") {
		t.Fatalf("rules = %q", rules)
	}
	for _, want := range []string{"`oldText`", "`newText`", "original file", "overlapping edits are rejected", "`content`", "create or overwrite"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing edit contract %q in %q", want, rules)
		}
	}
	rules = strings.Join(toolRules([]toolcore.ToolSpec{{Name: "apply_patch", Parameters: map[string]any{"type": "object"}}, {Name: "edit", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}}}), "\n")
	if !strings.Contains(rules, "Use apply_patch") {
		t.Fatalf("rules = %q", rules)
	}
}
