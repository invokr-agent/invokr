package agentui

import (
	"slices"
	"strings"
	"testing"
)

func resetDynamic(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { RegisterDynamicCommands(nil) })
}

func TestRegisterDynamicCommandsAddsAndSkipsCollisions(t *testing.T) {
	resetDynamic(t)
	RegisterDynamicCommands([]DynamicCommand{
		{Name: "browse", Usage: "/browse url=…", Summary: "drive the browser", Kind: "tool"},
		{Name: "help", Usage: "/help", Summary: "collides with built-in", Kind: "tool"},
		{Name: "skill:review", Usage: "/skill:review", Summary: "review code", Kind: "skill"},
	})
	if help := findCommand("help"); help == nil || help.summary == "collides with built-in" {
		t.Fatal("built-in /help was shadowed by a dynamic command")
	}
	if findCommand("browse") == nil || findCommand("skill:review") == nil {
		t.Fatal("dynamic commands not registered")
	}
	if !slices.Contains(commandSuggestions(), "/skill:review") {
		t.Fatal("completion suggestions missing dynamic command")
	}
	if !strings.Contains(renderCommandHelp(0), "review code") {
		t.Fatal("/help output missing dynamic command summary")
	}
}

func TestRegisterDynamicCommandsReplacesPreviousBatch(t *testing.T) {
	resetDynamic(t)
	RegisterDynamicCommands([]DynamicCommand{{Name: "old", Kind: "tool"}})
	RegisterDynamicCommands([]DynamicCommand{{Name: "new", Kind: "tool"}})
	if findCommand("old") != nil {
		t.Fatal("stale dynamic command survived re-registration")
	}
	if findCommand("new") == nil {
		t.Fatal("re-registered command missing")
	}
}

func TestSkillPromptComposition(t *testing.T) {
	prompt := skillPrompt("  do the thing  ", []string{"on", "main"})
	if prompt != "Follow this skill:\n\ndo the thing\n\nTask: on main" {
		t.Fatalf("unexpected prompt %q", prompt)
	}
	if got := skillPrompt("x", nil); strings.Contains(got, "Task:") {
		t.Fatalf("no-arg prompt should have no task suffix: %q", got)
	}
}
