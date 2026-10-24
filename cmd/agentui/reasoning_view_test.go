package agentui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-ai/invokr/platform/events"
)

func reasoningEvent(text string) events.Event {
	return events.Event{Type: events.EventReasoning, Reasoning: &events.ReasoningPayload{Text: text}}
}

// TestReasoningShowsBetweenToolCalls: a step's reasoning lands in the log in
// full, ahead of the tool block it led to.
func TestReasoningShowsBetweenToolCalls(t *testing.T) {
	var c chatLog
	long := strings.Repeat("check the branch before pushing ", 20)
	c.ApplyEvent(reasoningEvent("  " + long + "\n"))
	c.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &events.ToolPayload{ID: "1", Name: "git_read"}})
	if c.Len() != 2 || c.messages[0].role != "reasoning" || c.messages[1].toolName != "git_read" {
		t.Fatalf("messages = %+v, want reasoning then the tool", c.messages)
	}
	var b strings.Builder
	writeMessage(&b, c.messages[0], 60, "")
	if got := strings.Join(strings.Fields(ansi.Strip(b.String())), " "); !strings.Contains(got, strings.TrimSpace(long)) {
		t.Fatalf("reasoning was cut: %q", got)
	}
}

// TestReasoningGoesAboveStreamedAnswer: on a final turn the answer streams
// first and the reasoning arrives after, but it still reads above the answer.
func TestReasoningGoesAboveStreamedAnswer(t *testing.T) {
	var c chatLog
	c.ApplyEvent(events.Event{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: "Done."}})
	c.ApplyEvent(reasoningEvent("wrap up"))
	if c.Len() != 2 || c.messages[0].role != "reasoning" || c.messages[1].text != "Done." || !c.messages[1].streaming {
		t.Fatalf("messages = %+v, want reasoning above the streaming answer", c.messages)
	}
}

func TestBlankReasoningIsNotAToolEvent(t *testing.T) {
	if isToolEvent(reasoningEvent(" \n")) || !isToolEvent(reasoningEvent("x")) {
		t.Fatal("blank reasoning must be dropped, non-blank kept")
	}
}
