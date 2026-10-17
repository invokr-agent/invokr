package agentui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
)

func TestResumedUIShowsSavedFailedCheckAsHistorical(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	check := turn.ExecutionEvidence{Tool: "shell", CallID: "saved-check", Command: "go test ./saved", Outcome: "flagged", Code: "exit 1", ExitCode: new(1), Freshness: "current", Finished: 2}
	item := turn.TranscriptMessage{Role: "assistant", Text: "All checks passed", ExecutionEvidence: []turn.ExecutionEvidence{check}}
	m.chatLog.appendTranscript(item)
	shown := ansi.Strip(m.chatContent())
	for _, want := range []string{"All checks passed", "go test ./saved", "flagged:1", "historical"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("resumed UI lacks %q: %s", want, shown)
		}
	}
	if item.ExecutionEvidence[0].Freshness != "current" {
		t.Fatal("resumed UI mutated saved evidence")
	}
}

func TestResumedUIWithoutEvidenceKeepsLegacyText(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.chatLog.appendTranscript(turn.TranscriptMessage{Role: "assistant", Text: "hello"})
	if shown := ansi.Strip(m.chatContent()); !strings.Contains(shown, "hello") || strings.Contains(shown, "Execution history") {
		t.Fatalf("legacy transcript changed: %s", shown)
	}
}

func TestHiddenFinishedToolRetainsMessageIDs(t *testing.T) {
	log := newChatLog(progressOff)
	log.Append(message{role: "assistant", text: "before"})
	tool := &events.ToolPayload{ID: "call", Name: "shell"}
	log.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: tool})
	log.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: tool})
	log.Append(message{role: "assistant", text: "after"})
	if len(log.messages) != 2 || log.messages[0].id != 1 || log.messages[1].id != 3 {
		t.Fatalf("hidden tool changed message identity: %#v", log.messages)
	}
}
