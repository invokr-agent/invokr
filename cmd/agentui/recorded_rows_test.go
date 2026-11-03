package agentui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

// TestRecordedOverlayRowsShowEachCallOnce: the breakdown is one row per call
// (needs-a-look calls first, identical repeats counted on one row), and none of
// the escaping, tabs or argument tails that made the old listing unreadable
// survive into a row.
func TestRecordedOverlayRowsShowEachCallOnce(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.width = 96
	args := `{"command":"go test ./cmd/agentui 2>&1 | grep -c \"ok\"` + "\n\t" + strings.Repeat("2335", 40) + `"}`
	tool := events.ToolPayload{ID: "shell-1", Name: "shell", Arguments: args}
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &tool})
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolFailed, Tool: &events.ToolPayload{ID: tool.ID, Name: tool.Name, Code: "exit_1"}})
	for i := range 3 {
		read := events.ToolPayload{ID: "read-" + fmt.Sprint(i), Name: "read", Arguments: `{"path":"qa-bot4-manual/queue-a.txt"}`}
		m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &read})
		m.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &read})
	}
	m.updateResponse(responseMsg{text: "done", evidence: []turn.ExecutionEvidence{
		{CallID: "read-0", Tool: "read", Outcome: "completed", Finished: 1},
		{CallID: "read-1", Tool: "read", Outcome: "completed", Finished: 2},
		{CallID: "read-2", Tool: "read", Outcome: "completed", Finished: 3},
		{CallID: "shell-1", Tool: "shell", Outcome: "failed", Code: "exit_1", ExitCode: new(1), Freshness: "stale", Finished: 4},
	}})
	rows := strings.Split(ansi.Strip(m.recorded(120)), "\n")
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3 (header, failure, collapsed repeat):\n%s", len(rows), strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[1], "✕") || !strings.Contains(rows[1], "exit=1") || !strings.Contains(rows[1], "go test ./cmd/agentui") {
		t.Fatalf("failure row is not first or lacks the outcome: %q", rows[1])
	}
	if !strings.Contains(rows[2], "×3") || !strings.Contains(rows[2], "✓") || !strings.Contains(rows[2], "queue-a.txt") {
		t.Fatalf("repeat row = %q", rows[2])
	}
	if strings.Contains(m.recorded(120), "(stale)") || strings.Contains(m.recorded(120), "\\\"") || strings.Contains(m.recorded(120), "\t") {
		t.Fatalf("row noise survived: %q", m.recorded(120))
	}
	for _, row := range rows {
		if ansi.StringWidth(row) > 120 {
			t.Fatalf("row is wider than the popup shows: %d > %d", ansi.StringWidth(row), 120)
		}
	}
}

// TestMouseWheelScrollsTheOpenOverlay: with the breakdown up the wheel belongs
// to it, and the transcript behind keeps its place.
func TestMouseWheelScrollsTheOpenOverlay(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.width, m.height = 70, 20
	var evidence []turn.ExecutionEvidence
	for i := range 20 {
		evidence = append(evidence, turn.ExecutionEvidence{CallID: fmt.Sprintf("c-%02d", i), Tool: "read", Outcome: "completed", Finished: i + 1})
		call := events.ToolPayload{ID: fmt.Sprintf("c-%02d", i), Name: "read", Arguments: fmt.Sprintf(`{"path":"file-%02d.txt"}`, i)}
		m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &call})
		m.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &call})
	}
	m.updateResponse(responseMsg{text: "done", evidence: evidence})
	m.showRecorded()
	first := ansi.Strip(m.View().Content)
	if !strings.Contains(first, "file-00.txt") || strings.Contains(first, "file-19.txt") {
		t.Fatalf("the overlay does not start at its first row:\n%s", first)
	}
	m.userScrolled = false
	m.updateMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	scrolled := ansi.Strip(m.View().Content)
	if first == scrolled || !strings.Contains(scrolled, "file-05.txt") || strings.Contains(scrolled, "file-00.txt") {
		t.Fatalf("the wheel did not scroll the overlay:\n%s", scrolled)
	}
	if m.userScrolled {
		t.Fatal("the wheel moved the transcript behind the overlay")
	}
}

// TestRecordedShortcutClearsFooterNotice: the notice points at the breakdown,
// so opening it retires the notice for the turn.
func TestRecordedShortcutClearsFooterNotice(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.updateResponse(responseMsg{text: "done", evidence: []turn.ExecutionEvidence{{Tool: "shell", Outcome: "failed", Code: "exit_1", ExitCode: new(1)}}})
	if hint := ansi.Strip(m.helpHint()); !strings.Contains(hint, "needs a look") {
		t.Fatalf("footer notice missing before the overlay opens: %q", hint)
	}
	m.updateKey(testModifiedKey('r', tea.ModAlt))
	if !m.overlay.IsOpen() {
		t.Fatal("alt+r did not open the breakdown")
	}
	if m.recordedWarning != "" || strings.Contains(ansi.Strip(m.helpHint()), "needs a look") {
		t.Fatalf("footer notice still shown under the breakdown: warning=%q hint=%q", m.recordedWarning, ansi.Strip(m.helpHint()))
	}
}

// TestRecordedOverlayDrawsInsideItsBorder: nothing is painted past the box's
// right border, and the box stops above the input frame.
func TestRecordedOverlayDrawsInsideItsBorder(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.width, m.height = 72, 20
	long := "rm -rf " + strings.Repeat("deeply/nested/build/output/artifacts/", 12)
	args := fmt.Sprintf(`{"command":%q}`, long)
	call := events.ToolPayload{ID: "shell-1", Name: "shell", Arguments: args}
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &call})
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &call})
	m.updateResponse(responseMsg{text: "done", evidence: []turn.ExecutionEvidence{{CallID: call.ID, Tool: "shell", Outcome: "completed", Finished: 1}}})
	m.showRecorded()
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	var popupTop, popupBottom, inputTop int
	for i, line := range lines {
		if strings.Contains(line, "┐") && popupTop == 0 {
			popupTop = i
		}
		if strings.Contains(line, "┘") && popupBottom == 0 && i > popupTop {
			popupBottom = i
		}
		if strings.Contains(line, "╮") && inputTop == 0 && i > popupTop {
			inputTop = i
		}
	}
	if popupTop == 0 || popupBottom == 0 || inputTop == 0 {
		t.Fatalf("no popup border or input frame in:\n%s", strings.Join(lines, "\n"))
	}
	col := columnOf(lines[popupTop], "┐")
	for i := popupTop; i <= popupBottom; i++ {
		edge := strings.Split(lines[i], "")
		if col >= len(edge) || !strings.Contains("│┐┘", edge[col]) {
			t.Fatalf("row %d is drawn past the right border at column %d: %q", i, col, lines[i])
		}
	}
	if inputTop <= popupBottom {
		t.Fatalf("the box's bottom row %d reaches the input frame at %d", popupBottom, inputTop)
	}
	// The clipped-at-the-border rows used to lose their whole outcome column; a
	// row that fits keeps it.
	if !strings.Contains(ansi.Strip(m.View().Content), "completed") {
		t.Fatalf("the row's outcome column was cut off:\n%s", strings.Join(lines, "\n"))
	}
}

// columnOf is the display column a border glyph sits on, counted in characters
// so a wide glyph earlier in the row cannot shift the answer.
func columnOf(line, needle string) int {
	for i, ch := range strings.Split(line, "") {
		if ch == needle {
			return i
		}
	}
	return -1
}

// A heredoc or multi-line command is still one row.
func TestRecordedRowFlattensMultiLineCommand(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.updateResponse(responseMsg{text: "done", evidence: []turn.ExecutionEvidence{
		{CallID: "s", Tool: "shell", Command: "cat <<'EOF' > x.txt\nhello\nEOF", Outcome: "completed", Finished: 1},
	}})
	rows := strings.Split(ansi.Strip(m.recorded(120)), "\n")
	if len(rows) != 2 || !strings.Contains(rows[1], "cat <<'EOF' > x.txt hello EOF") {
		t.Fatalf("multi-line command rows = %q", rows)
	}
}
