package agentui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/surface/ui"
)

func settleShell(log *chatLog, id, output string) {
	payload := &events.ToolPayload{ID: id, Name: "shell", Arguments: `{"command":"ls"}`}
	log.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: payload, Timestamp: time.Now()})
	done := *payload
	done.Output = output
	log.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &done})
}

func manyLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "out" + strings.Repeat("x", i%3)
	}
	return strings.Join(lines, "\n")
}

func TestFoldToggleExpandsFullOutput(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	settleShell(&m.chatLog, "f1", manyLines(12))
	m.syncChatViewport(true)

	collapsed := ansi.Strip(m.chatContent())
	if !strings.Contains(collapsed, "▸ 7 more lines") {
		t.Fatalf("collapsed block has no fold indicator:\n%s", collapsed)
	}

	if !m.updateFocusKey(testModifiedKey('x', tea.ModAlt)) { // alt+x
		t.Fatal("alt+x not consumed")
	}
	if !m.focusMode {
		t.Fatal("focus mode not entered")
	}
	if !strings.Contains(m.chatContent(), ui.Invokr.Warn.Bold(true).Render("◀")) {
		t.Fatal("details cursor is not highlighted in the warning palette color")
	}
	if !m.updateFocusKey(testKey(tea.KeyEnter)) { // enter -> toggle
		t.Fatal("enter not consumed in focus mode")
	}
	expanded := ansi.Strip(m.chatContent())
	if strings.Contains(expanded, "more lines") {
		t.Fatalf("expanded block still folded:\n%s", expanded)
	}
	if strings.Count(expanded, "out") < 12 {
		t.Fatalf("expanded block missing output lines:\n%s", expanded)
	}
	// esc returns to the prompt.
	if !m.updateFocusKey(testKey(tea.KeyEscape)) {
		t.Fatal("esc not consumed in focus mode")
	}
	if m.focusMode {
		t.Fatal("focus mode not exited")
	}
	// The fold state itself survives leaving focus mode.
	if again := ansi.Strip(m.chatContent()); strings.Contains(again, "more lines") {
		t.Fatalf("block re-collapsed on exit:\n%s", again)
	}
}

func TestFoldStateSurvivesStreamingUpdates(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	settleShell(&m.chatLog, "f2", manyLines(10))
	m.toggleFold(0)
	if !m.chatLog.messages[0].expanded {
		t.Fatal("toggle did not expand")
	}
	// A later assistant delta (streaming) must not collapse the block.
	m.chatLog.ApplyEvent(events.Event{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: "answer"}})
	if !m.chatLog.messages[0].expanded {
		t.Fatal("streaming update reset the fold state")
	}
}

func TestFocusNavigationMovesBetweenBlocks(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	settleShell(&m.chatLog, "n1", "one")
	m.chatLog.Append(message{role: "assistant", text: "hi"})
	settleShell(&m.chatLog, "n2", "two")
	m.syncChatViewport(true)
	m.updateFocusKey(testModifiedKey('x', tea.ModAlt))
	if m.focusMsg != 2 {
		t.Fatalf("focus starts at %d, want the last block (2)", m.focusMsg)
	}
	m.updateFocusKey(testKey('k'))
	if m.focusMsg != 0 {
		t.Fatalf("k moved focus to %d, want 0 (skipping the assistant turn)", m.focusMsg)
	}
	m.updateFocusKey(testKey('j'))
	if m.focusMsg != 2 {
		t.Fatalf("j moved focus to %d, want 2", m.focusMsg)
	}
}

func TestClickFoldTogglesOnHeaderLine(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	settleShell(&m.chatLog, "c1", manyLines(9))
	m.syncChatViewport(true)
	start, _, ok := m.chat.ItemRange(messageItemID(m.chatLog.messages[0].id))
	if !ok {
		t.Fatal("fold block not visible")
	}
	header := start + 1
	if !m.clickFold(header) {
		t.Fatal("click on the header line did not toggle")
	}
	if !m.chatLog.messages[0].expanded {
		t.Fatal("block not expanded after click")
	}
	// A click below the block does nothing.
	if m.clickFold(header+2) && m.chatLog.messages[0].expanded {
		t.Fatal("click outside a header toggled")
	}
}

func TestResponseHistoryOpensOnDemand(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &events.ToolPayload{ID: "old-patch", Name: "read", Arguments: `{"paths":["missing.txt"]}`}})
	m.chatLog.Append(message{role: "assistant", text: "streamed draft", streaming: true})
	response := responseMsg{
		text:     "Patch applied; focused check completed.",
		failures: []turn.ToolOutcome{{Tool: "apply_patch", Kind: "error", Detail: "old missing context"}},
		evidence: []turn.ExecutionEvidence{
			{Tool: "shell", CallID: "opaque-check-id", Command: "go test ./tools", Outcome: "completed", Freshness: "current", ExitCode: new(0), Finished: 3},
			{Tool: "apply_patch", CallID: "old-patch", Outcome: "failed", Freshness: "stale", Finished: 1},
		},
	}
	m.updateResponse(response)
	collapsed := ansi.Strip(m.chatContent())
	if strings.Contains(collapsed, "Recorded:") || strings.Contains(collapsed, "old missing context") || strings.Contains(collapsed, "streamed draft") {
		t.Fatalf("execution report leaked into response:\n%s", collapsed)
	}
	if warning := ansi.Strip(m.helpHint()); !strings.Contains(warning, "2 calls need a look") {
		t.Fatalf("failure warning hidden: %s", warning)
	}
	m.showRecorded()
	expanded := ansi.Strip(m.overlay.Content())
	for _, want := range []string{"Execution history", "go test ./tools", "failed", "old missing context", "missing.txt", "exit=0"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("overlay missing %q: %s", want, expanded)
		}
	}
	if response.evidence[0].CallID != "opaque-check-id" || response.evidence[0].Outcome != "completed" || response.evidence[0].Command != "go test ./tools" {
		t.Fatal("presentation changed the source ledger")
	}
	m.updateKey(testKey(tea.KeyEscape))
	if m.overlay.IsOpen() {
		t.Fatal("overlay did not close")
	}
}

func TestResponseHistoryDoesNotInventVerification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response responseMsg
		want     string
	}{
		{"legacy", responseMsg{text: "hello"}, "hello"},
		{"empty", responseMsg{text: "hello", evidence: []turn.ExecutionEvidence{}}, "hello"},
		{"failure without evidence", responseMsg{failures: []turn.ToolOutcome{{Tool: "read", Kind: "denied", Detail: "denial"}}}, "read denied"},
		{"flagged latest", responseMsg{evidence: []turn.ExecutionEvidence{
			{Tool: "shell", Outcome: "completed", ExitCode: new(0), Finished: 1},
			{Tool: "shell", Outcome: "flagged", ExitCode: new(1), Finished: 2},
		}}, "exit=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModelWithHiddenLogs()
			m.updateResponse(tc.response)
			got := ansi.Strip(m.recorded)
			if tc.name == "legacy" || tc.name == "empty" {
				got = ansi.Strip(m.chatContent())
			}
			if !strings.Contains(got, tc.want) || strings.Contains(got, "task verified") {
				t.Fatalf("unexpected summary:\n%s", got)
			}
			if tc.name == "flagged latest" && !strings.Contains(ansi.Strip(m.helpHint()), "1 call needs a look") {
				t.Fatalf("styled outcome lost the flagged count:\n%s", got)
			}
			if (tc.name == "legacy" || tc.name == "empty") && strings.Contains(got, "Execution history") {
				t.Fatal("invented history for legacy response")
			}
		})
	}
}
