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
	// The saved call comes back as a failed tool block above the answer, with
	// no bookkeeping line (A11, A13).
	for _, want := range []string{"All checks passed", "go test ./saved", "✕"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("resumed UI lacks %q: %s", want, shown)
		}
	}
	if strings.Contains(shown, "map[") || strings.Contains(shown, "Execution evidence") {
		t.Fatalf("resumed UI shows bookkeeping: %s", shown)
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

// TestResumedInterruptedTurnShowsAsInterrupted: a prompt stopped before it
// answered comes back with its prompt and an interrupted marker (A14).
func TestResumedInterruptedTurnShowsAsInterrupted(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.chatLog.appendTranscript(turn.TranscriptMessage{Role: "user", Text: "add a done command"})
	m.chatLog.appendTranscript(turn.TranscriptMessage{Role: "assistant", Interrupted: true})
	shown := ansi.Strip(m.chatContent())
	if !strings.Contains(shown, "add a done command") || !strings.Contains(shown, "interrupted before an answer") {
		t.Fatalf("interrupted turn not shown: %s", shown)
	}
}
