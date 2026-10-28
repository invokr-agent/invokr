package agentui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

func TestEvidenceOverlayUsesQueuedCallsFromCurrentTurnOnly(t *testing.T) {
	queue := newToolEventQueue()
	m := newModel(context.Background(), &fakeSession{}, queue, nil, nil, false, nil, nil)
	for _, tool := range []events.ToolPayload{
		{ID: "edit-b", Name: "edit", Arguments: `{"path":"qa-bot4-manual/queue-b.txt","content":"B"}`},
		{ID: "read-ab", Name: "read", Arguments: `{"paths":["qa-bot4-manual/queue-a.txt","qa-bot4-manual/queue-b.txt"]}`},
	} {
		item := tool
		queue.Push(events.Event{Type: events.EventToolStarted, Tool: &item})
		queue.Push(events.Event{Type: events.EventToolCompleted, Tool: &item})
	}
	m.drainToolEvents()
	m.updateResponse(responseMsg{text: "Created B", evidence: []turn.ExecutionEvidence{
		{CallID: "edit-b", Tool: "edit", Outcome: "completed", Finished: 2},
		{CallID: "read-ab", Tool: "read", Outcome: "completed", Finished: 4},
	}})
	if shown := ansi.Strip(m.chatContent()); !strings.Contains(shown, "Created B") || strings.Contains(shown, "Recorded:") {
		t.Fatalf("response still contains execution summary: %s", shown)
	}
	m.updateKey(testModifiedKey('r', tea.ModAlt))
	if got := ansi.Strip(m.overlay.Content()); !m.overlay.IsOpen() || !strings.Contains(got, "edit") || !strings.Contains(got, "queue-b.txt") || !strings.Contains(got, "queue-a.txt") {
		t.Fatalf("current-call overlay missing queued arguments: open=%v content=%q", m.overlay.IsOpen(), got)
	}
	m.updateKey(testKey(tea.KeyEscape))
	if m.overlay.IsOpen() {
		t.Fatal("Esc did not close recorded-call overlay")
	}
	m.input.SetValue("next request")
	m.submit()
	m.updateResponse(responseMsg{text: "No tools needed"})
	m.runCommand("/recorded")
	if got := ansi.Strip(m.overlay.Content()); !m.overlay.IsOpen() || !strings.Contains(got, "No tool calls recorded for this request") || strings.Contains(got, "queue-b.txt") {
		t.Fatalf("new zero-tool turn reused old details: open=%v content=%q", m.overlay.IsOpen(), got)
	}
}

func TestEvidenceOverlayResumeDoesNotInventPaths(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.appendSavedTranscript(turn.TranscriptMessage{Role: "assistant", Text: "Created B", ExecutionEvidence: []turn.ExecutionEvidence{{CallID: "old-edit", Tool: "edit", Outcome: "completed"}}})
	m.updateKey(testModifiedKey('r', tea.ModAlt))
	if got := ansi.Strip(m.overlay.Content()); !m.overlay.IsOpen() || !strings.Contains(got, "Historical — not current verification") || !strings.Contains(got, "edit") || strings.Contains(got, "queue-b.txt") {
		t.Fatalf("resume overlay claimed unavailable paths: open=%v content=%q", m.overlay.IsOpen(), got)
	}
	m.overlay.Close()
	m.appendSavedTranscript(turn.TranscriptMessage{Role: "assistant", Text: "Later answer"})
	m.updateKey(testModifiedKey('r', tea.ModAlt))
	if got := ansi.Strip(m.overlay.Content()); !strings.Contains(got, "Historical call details unavailable") || strings.Contains(got, "old-edit") {
		t.Fatalf("latest saved turn borrowed earlier evidence: %s", got)
	}
}

func TestEvidenceOverlayDoesNotBorrowPriorToolID(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	tool := events.ToolPayload{ID: "same", Name: "edit", Arguments: `{"path":"old-secret.txt"}`}
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &tool})
	m.turnStart = m.chatLog.nextID
	m.updateResponse(responseMsg{text: "done", evidence: []turn.ExecutionEvidence{{CallID: "same", Tool: "edit", Outcome: "completed"}}})
	if got := ansi.Strip(m.recorded); strings.Contains(got, "old-secret.txt") {
		t.Fatalf("borrowed prior-turn argument: %s", got)
	}
}

func TestEvidenceOverlayRetainsInterruptedEvidence(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.updateResponse(responseMsg{err: context.Canceled, evidence: []turn.ExecutionEvidence{{Tool: "shell", Command: "go test ./...", Outcome: "failed", Code: "canceled"}}})
	if got := ansi.Strip(m.recorded); !strings.Contains(got, "go test ./...") || !strings.Contains(got, "canceled") {
		t.Fatalf("interrupted evidence lost: %s", got)
	}
	if got := ansi.Strip(m.helpHint()); !strings.Contains(got, "Interrupted") {
		t.Fatalf("interruption warning hidden: %s", got)
	}
}

func TestEvidenceOverlayRespectsPickerAndNarrowFooter(t *testing.T) {
	m := openModelPicker(t, &fakeSession{model: "m1", models: []string{"m1", "m2"}})
	m.updateKey(testModifiedKey('r', tea.ModAlt))
	if !m.modelPicker.open || m.overlay.IsOpen() {
		t.Fatal("recorded shortcut displaced model picker")
	}
	m.modelPicker.open = false
	m.width = 40
	m.recordedWarning = "Interrupted · 12 recorded calls · tool denied · Alt+R"
	footer := ansi.Strip(m.footer())
	if ansi.StringWidth(footer) > m.mainWidth() || !strings.Contains(footer, "Alt+R") || !strings.Contains(footer, "Interrupt") {
		t.Fatalf("narrow warning is missing or overflows: width=%d footer=%q", m.mainWidth(), footer)
	}
}

func TestEvidenceOverlayCancellationUsesQueuedTerminalEvents(t *testing.T) {
	queue := newToolEventQueue()
	m := newModel(context.Background(), &fakeSession{err: context.Canceled}, queue, nil, nil, false, nil, nil)
	m.input.SetValue("check status")
	_, send := m.submit()
	tool := events.ToolPayload{ID: "check", Name: "shell", Arguments: `{"command":"go test ./..."}`}
	queue.Push(events.Event{Type: events.EventToolStarted, Tool: &tool})
	queue.Push(events.Event{Type: events.EventToolFailed, Tool: &events.ToolPayload{ID: tool.ID, Name: tool.Name, Code: "canceled", Error: "context canceled"}})
	response := send().(tea.BatchMsg)[0]().(responseMsg) // runner.failedResponse has no ledger
	if len(response.evidence) != 0 || !errors.Is(response.err, context.Canceled) {
		t.Fatalf("test session did not reproduce nil-ledger cancellation: %+v", response)
	}
	m.updateResponse(response)
	got := ansi.Strip(m.recorded)
	if !strings.Contains(got, "Interrupted — incomplete") || !strings.Contains(got, "go test ./...") || !strings.Contains(got, "canceled") {
		t.Fatalf("canceled request lost observed terminal call: %s", got)
	}
	if hint := ansi.Strip(m.helpHint()); !strings.Contains(hint, "Interrupted") || !strings.Contains(hint, "1 recorded") || !strings.Contains(hint, "Alt+R") {
		t.Fatalf("canceled request hid recorded count: %s", hint)
	}
}

func TestEvidenceOverlayKeepsDenialWarningVisible(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.updateResponse(responseMsg{text: "done", evidence: []turn.ExecutionEvidence{{Tool: "shell", Outcome: "denied", Code: "approval_denied"}}})
	if got := ansi.Strip(m.helpHint()); !strings.Contains(got, "1 denied") || !strings.Contains(got, "Alt+R") {
		t.Fatalf("denial warning hidden outside overlay: %s", got)
	}
	if got := ansi.Strip(m.chatContent()); strings.Contains(got, "Recorded:") || !strings.Contains(got, "done") {
		t.Fatalf("assistant response changed: %s", got)
	}
}

func TestEvidenceOverlayDoesNotStealApprovalAndPlainKeepsGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)
	done := make(chan struct{}, 1)
	go func() {
		_, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell", Subject: "echo held"})
		done <- struct{}{}
	}()
	waitForQueueLen(t, m.approvalQueue, 1)
	m.updateKey(testModifiedKey('r', tea.ModAlt))
	if m.overlay.IsOpen() {
		t.Fatal("evidence overlay obscured pending approval")
	}
	m.updateKey(testTextKey("n"))
	<-done

	var out bytes.Buffer
	renderer := plainResponseRenderer{out: &out, errOut: &bytes.Buffer{}}
	renderer.finish(turn.AgentResponse{Message: "Created B", ExecutionEvidence: []turn.ExecutionEvidence{{Tool: "edit", Outcome: "denied"}}})
	if got := out.String(); !strings.Contains(got, "Created B") || !strings.Contains(got, "Execution history") || !strings.Contains(got, "denied") {
		t.Fatalf("plain mode lost visible execution guard: %s", got)
	}
	if got, ok := plainCommand(context.Background(), nil, &fakeSession{}, "/recorded"); !ok || !strings.Contains(got, "unavailable") {
		t.Fatalf("plain overlay command lied: %q, %v", got, ok)
	}
}

// TestRecordedWarningKeepsKeyHints: a turn's warning leads the footer but the
// key hints stay beside it rather than vanishing until the next turn.
func TestRecordedWarningKeepsKeyHints(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.width = 160
	m.recordedWarning = "1 call needs a look · Alt+R for recorded calls"
	if footer := ansi.Strip(m.helpHint()); !strings.Contains(footer, "Alt+R") || !strings.Contains(footer, "send") {
		t.Fatalf("footer = %q, want the warning and the key hints", footer)
	}
}

// TestExecutionWarningCountsEachCallOnce: a failed call is in both the
// failures and the evidence, and a denial is evidence with a denied code.
func TestExecutionWarningCountsEachCallOnce(t *testing.T) {
	failures := []turn.ToolOutcome{{Tool: "read"}, {Tool: "shell"}}
	evidence := []turn.ExecutionEvidence{{Tool: "read", Outcome: "failed"}, {Tool: "shell", Outcome: "failed", Code: "denied"}, {Tool: "edit", Outcome: "completed"}}
	if got := executionWarning(failures, evidence); !strings.HasPrefix(got, "2 calls need a look (1 denied)") {
		t.Fatalf("warning = %q", got)
	}
	if got := executionWarning(failures[:1], nil); !strings.HasPrefix(got, "1 call needs a look") {
		t.Fatalf("failures-only warning = %q", got)
	}
}
