package agentui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-agent/invokr/platform/events"
)

// TestCancelledToolBlockSettles guards issue #300: a cancelled tool call must
// leave the transcript block in a terminal state with a frozen elapsed time,
// rendered with the stop glyph rather than the running spinner.
func TestCancelledToolBlockSettles(t *testing.T) {
	log := newChatLog(progressAll)
	started := time.Now().Add(-3 * time.Second)
	log.ApplyEvent(events.Event{
		Type: events.EventToolStarted, Timestamp: started,
		Tool: &events.ToolPayload{ID: "call-1", Name: "code_interpreter", Arguments: `{"code":"time.sleep(90)"}`},
	})
	if last := log.Last(); last == nil || last.toolStatus != toolRunning {
		t.Fatalf("started event did not open a running block: %#v", log.Last())
	}

	log.ApplyEvent(events.Event{
		Type: events.EventToolFailed,
		Tool: &events.ToolPayload{ID: "call-1", Name: "code_interpreter", Code: "canceled", Duration: 3 * time.Second, Error: "context canceled"},
	})

	last := log.Last()
	if last == nil || last.toolStatus == toolRunning {
		t.Fatalf("cancelled block is still running: %#v", last)
	}

	line := ansi.Strip(renderToolBlock(*last, "◐", 80))
	if !strings.Contains(line, "cancelled") || !strings.Contains(line, glyphStop) {
		t.Fatalf("cancelled tool line = %q", line)
	}
	if strings.Contains(line, "running") {
		t.Fatalf("cancelled tool line still reports running: %q", line)
	}
}

// TestCancelledToolPlainRendererUnchanged: the plain (non-TUI) renderer has no
// in-place block to settle, so a cancelled call stays one standalone line.
func TestCancelledToolPlainRendererUnchanged(t *testing.T) {
	line := ansi.Strip(renderToolEvent(events.Event{
		Type: events.EventToolFailed,
		Tool: &events.ToolPayload{ID: "call-1", Name: "shell", Code: "canceled", Duration: time.Second},
	}))
	if strings.Contains(line, "\n") || !strings.Contains(line, "canceled") {
		t.Fatalf("plain cancelled tool line = %q", line)
	}
}
