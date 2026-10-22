package agentui

import (
	"context"
	"testing"

	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools/system/shell"
)

func TestGHSandboxDenialSuggestsToolboxToolOnce(t *testing.T) {
	m := newModel(context.Background(), nil, newToolEventQueue(), nil, nil, false, nil, nil)
	denied := events.Event{Type: events.EventToolCompleted, Tool: &events.ToolPayload{Name: "shell", Code: "denied", Output: shell.GHSandboxDenial}}
	m.toolEvents.Push(events.Event{Type: events.EventToolCompleted, Tool: &events.ToolPayload{Name: "shell", Output: "ok"}})
	m.drainToolEvents()
	if m.pendingToolSuggestion != "" {
		t.Fatal("suggested without a gh denial")
	}
	m.toolEvents.Push(denied)
	m.drainToolEvents()
	if m.pendingToolSuggestion != shell.GHToolboxTool {
		t.Fatalf("pending = %q", m.pendingToolSuggestion)
	}
	m.openToolSuggestion()
	m.overlay.Close()
	m.toolEvents.Push(denied)
	m.drainToolEvents()
	if m.pendingToolSuggestion != "" {
		t.Fatal("suggested twice in one session")
	}
}
