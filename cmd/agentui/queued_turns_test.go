package agentui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Enter while a turn runs queues the message instead of dropping it, and the
// queue flushes oldest-first when the turn ends.
func TestModel_QueuedMessagesRenderAndFlushInOrder(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, &fakePromptHistory{}, nil)
	m.sending = true
	for _, text := range []string{"first", "second"} {
		m.input.SetValue(text)
		updated, cmd := m.updateKey(testKey(tea.KeyEnter))
		m = updated.(*model)
		// Queueing is state, not work: the send waits for the turn to end.
		if cmd != nil {
			t.Fatalf("queueing runs no command, got one")
		}
	}
	// The queue keeps the order the messages were typed in.
	if fmt.Sprintf("%v", m.queued) != "[first second]" {
		t.Fatalf("queued = %#v, want [first second]", m.queued)
	}
	// Enter hands the input back empty, and nothing reached the session.
	if m.input.Value() != "" || len(session.calls) != 0 {
		t.Fatalf("input = %q, calls = %#v", m.input.Value(), session.calls)
	}

	// The queue block rides above the input box: a count header, then one dimmed
	// numbered line per message, in send order. chatVisibleLines counts those
	// rows, so the transcript gives the space up instead of the queue drawing
	// over it.
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "2 queued \u00b7 sent when this turn ends") ||
		!strings.Contains(view, "1 \u2981 first") || !strings.Contains(view, "2 \u2981 second") {
		t.Fatalf("queue block missing:\n%s", view)
	}

	// One queued message per turn end, oldest first, so each response can
	// react to the message before it. The send is a command the caller runs,
	// so the queued text shows here as that turn's user line in the transcript.
	for _, want := range []string{"first", "second"} {
		_, cmd := m.Update(responseMsg{text: "ok"})
		if cmd == nil {
			t.Fatalf("turn end with a queued message must send, got nil cmd")
		}
		last := m.chatLog.Last()
		if last == nil {
			t.Fatalf("no user line, want %q", want)
		}
		if last.role != "user" || last.text != want {
			t.Fatalf("last message = %q, want user %q", last.text, want)
		}
	}
	// The queue drained fully, so later views draw no block at all.
	if len(m.queued) != 0 {
		t.Fatalf("queued = %#v, want empty", m.queued)
	}
	// The flush sends queued text, never the draft, so the input stayed empty.
	if m.input.Value() != "" {
		t.Fatalf("input = %q, want empty", m.input.Value())
	}
}

// Up-arrow on an empty input pulls the newest queued message back for editing,
// and it leaves the queue, so Enter re-queues the one edit rather than sending
// a second copy of the original.
func TestModel_UpPullsQueuedMessageBackForEditing(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, &fakePromptHistory{}, nil)
	m.sending = true
	for _, text := range []string{"first", "second"} {
		m.input.SetValue(text)
		m.updateKey(testKey(tea.KeyEnter))
	}
	// Up on an empty input, mid-turn, reads the queue rather than history.
	m.updateKey(testKey(tea.KeyUp))
	// The pull hands the newest message back whole, at the caret's end, and
	// drops it from the queue, so it cannot be sent twice.
	if m.input.Value() != "second" || fmt.Sprintf("%v", m.queued) != "[first]" {
		t.Fatalf("input = %q, queued = %#v", m.input.Value(), m.queued)
	}
	// Enter re-queues the edited draft at the tail, after the older message.
	m.input.SetValue("second (edited)")
	m.updateKey(testKey(tea.KeyEnter))
	if fmt.Sprintf("%v", m.queued) != "[first second (edited)]" {
		t.Fatalf("queued = %#v, want [first second (edited)]", m.queued)
	}
	// A fresh draft has to survive the send the turn end starts.
	m.input.SetValue("draft in flight")
	_, cmd := m.Update(responseMsg{text: "ok"})
	if cmd == nil {
		t.Fatalf("turn end with a queued message must send, got nil cmd")
	}
	// The flush sent the older message, so it is the newest user line.
	last := m.chatLog.Last()
	if last == nil || last.role != "user" || last.text != "first" {
		t.Fatalf("last transcript line is not the queued user line")
	}
	// The draft is untouched, and the edited message is still queued.
	if m.input.Value() != "draft in flight" || fmt.Sprintf("%v", m.queued) != "[second (edited)]" {
		t.Fatalf("input = %q, queued = %#v", m.input.Value(), m.queued)
	}
}

// Queuing leaves the interrupt alone: mid-turn ctrl+c still cancels the turn
// in flight and keeps the UI open, and the canceled turn end neither drops nor
// flushes the queue.
func TestModel_QueuedMessagesLeaveInterruptUnchanged(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, &fakePromptHistory{}, nil)
	m.sending = true
	m.queued = []string{"later"}
	cancelled := false
	m.cancelSend = func() { cancelled = true }
	_, cmd := m.updateKey(testModifiedKey('c', tea.ModCtrl))
	// ctrl+c must not become a send or a quit while messages are queued.
	if isQuit(cmd) || !cancelled || !m.stopping {
		t.Fatalf("cancelled = %v, stopping = %v", cancelled, m.stopping)
	}
	// The canceled send comes back as a response, and that turn end must not
	// spend the queue: a stopped turn flushes nothing, so the queued message
	// survives and no send runs.
	_, _ = m.Update(responseMsg{err: context.Canceled})
	last := m.chatLog.Last()
	if fmt.Sprintf("%v", m.queued) != "[later]" || len(session.calls) != 0 || last == nil || last.text != "\u23f9 stopped" {
		t.Fatalf("stopped turn end: queued = %#v, calls = %#v, last = %#v", m.queued, session.calls, last)
	}
	// esc is the same handler, so an esc with a queue pending behaves the same.
	m.stopping = false
	_, cmd = m.updateKey(testKey(tea.KeyEscape))
	if isQuit(cmd) || fmt.Sprintf("%v", m.queued) != "[later]" {
		// The queue is not esc's to clear.
		t.Fatalf("esc with a queue should interrupt, queued = %#v", m.queued)
	}
	_, _ = m.Update(responseMsg{err: context.Canceled})
	last = m.chatLog.Last()
	if fmt.Sprintf("%v", m.queued) != "[later]" || len(session.calls) != 0 || last == nil || last.text != "\u23f9 stopped" {
		t.Fatalf("esc turn end: queued = %#v, calls = %#v, last = %#v", m.queued, session.calls, last)
	}
}
