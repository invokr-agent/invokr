package agentui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-ai/invokr/platform/events"
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

	if !m.updateFocusKey(testModifiedKey('x', tea.ModCtrl)) { // ctrl+x
		t.Fatal("ctrl+x not consumed")
	}
	if !m.focusMode {
		t.Fatal("focus mode not entered")
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
	m.updateFocusKey(testModifiedKey('x', tea.ModCtrl))
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
	if len(m.blockRanges) != 1 {
		t.Fatalf("blockRanges = %d, want 1", len(m.blockRanges))
	}
	header := m.blockRanges[0].start - m.chat.YOffset()
	if !m.clickFold(header) {
		t.Fatal("click on the header line did not toggle")
	}
	if !m.chatLog.messages[0].expanded {
		t.Fatal("block not expanded after click")
	}
	// A click below the block does nothing.
	if m.clickFold(m.blockRanges[0].end-m.chat.YOffset()+2) && m.chatLog.messages[0].expanded {
		t.Fatal("click outside a header toggled")
	}
}
