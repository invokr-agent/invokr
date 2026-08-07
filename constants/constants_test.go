package constants

import (
	"slices"
	"testing"
)

func TestShellAllowedCommandsAddsReadonlyBaseline(t *testing.T) {
	got := ShellAllowedCommands([]string{"ls", "pwd", "go", "gofmt"})
	for _, want := range []string{"find", "head", "tail", "cat", "grep", "gofmt"} {
		if !slices.Contains(got, want) {
			t.Fatalf("effective allowlist %v missing %q", got, want)
		}
	}
}

func TestShellAllowedCommandsEmptyUsesDefault(t *testing.T) {
	if got := ShellAllowedCommands(nil); len(got) != len(DefaultShellAllowedCommands()) {
		t.Fatalf("empty config should yield the default list, got %v", got)
	}
}

func TestCopyHelpersProtectMutableDefaults(t *testing.T) {
	shellCommands := DefaultShellAllowedCommands()
	shellCommands[0] = "mutated"
	if DefaultShellAllowedCommands()[0] == "mutated" {
		t.Fatal("DefaultShellAllowedCommands exposed mutable defaults")
	}

	providers := DefaultWebSearchProviders()
	providers[0] = "mutated"
	if DefaultWebSearchProviders()[0] == "mutated" {
		t.Fatal("DefaultWebSearchProviders exposed mutable defaults")
	}

	mappings := DefaultCustomHTTPSearchMappings()
	mappings["title"] = "mutated"
	if DefaultCustomHTTPSearchMappings()["title"] == "mutated" {
		t.Fatal("DefaultCustomHTTPSearchMappings exposed mutable defaults")
	}

	globs := DefaultApplyPatchSensitivePathGlobs()
	globs[0] = "mutated"
	if DefaultApplyPatchSensitivePathGlobs()[0] == "mutated" {
		t.Fatal("DefaultApplyPatchSensitivePathGlobs exposed mutable defaults")
	}
}

func TestEagerSchemaToolsIncludesTmux(t *testing.T) {
	if !slices.Contains(EagerSchemaTools, "tmux") {
		t.Fatalf("EagerSchemaTools %v missing tmux (#541)", EagerSchemaTools)
	}
}
