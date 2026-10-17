package agentui

import (
	"github.com/invokr-ai/invokr/platform/events"
	"testing"
)

func TestProgressMatchesCallID(t *testing.T) {
	c := newChatLog(progressAll)
	for _, id := range []string{"first", "second"} {
		c.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &events.ToolPayload{ID: id, Name: "demo"}})
	}
	for _, tc := range []struct{ id, text, first, second string }{
		{"first", "a", "a", ""}, {"missing", "ignored", "a", ""}, {"second", "b", "a", "b"}, {"", "legacy", "a", "legacy"},
	} {
		c.ApplyEvent(events.Event{Type: events.EventToolProgress, Tool: &events.ToolPayload{ID: tc.id, Name: "demo", Progress: tc.text}})
		if c.messages[0].toolProgressLine != tc.first || c.messages[1].toolProgressLine != tc.second {
			t.Fatalf("id %q: progress crossed calls", tc.id)
		}
	}
	c.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &events.ToolPayload{ID: "first", Name: "demo"}})
	c.ApplyEvent(events.Event{Type: events.EventToolProgress, Tool: &events.ToolPayload{ID: "first", Name: "demo", Progress: "late"}})
	if c.messages[1].toolProgressLine != "legacy" {
		t.Fatal("late event attributed to other call")
	}
}
