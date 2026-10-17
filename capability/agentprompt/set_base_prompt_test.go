package agentprompt

import (
	"context"
	"strings"
	"testing"
)

// TestSetBasePromptAppliesToNextBuild verifies /profile's prompt swap: a
// Build after SetBasePrompt renders the new base prompt, not the old one.
func TestSetBasePromptAppliesToNextBuild(t *testing.T) {
	builder, err := NewBuilder(Config{BasePrompt: "Old prompt."}, testDeps(Deps{}))
	if err != nil {
		t.Fatalf("NewBuilder() error = %v", err)
	}

	prompt, err := builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hi"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !strings.Contains(prompt.System, "Old prompt.") {
		t.Fatalf("system prompt missing old base: %q", prompt.System)
	}

	builder.SetBasePrompt("New prompt.")
	prompt, err = builder.Build(context.Background(), Request{SessionID: "s1", UserMessage: "hi"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !strings.Contains(prompt.System, "New prompt.") || strings.Contains(prompt.System, "Old prompt.") {
		t.Fatalf("system prompt did not switch: %q", prompt.System)
	}
}
