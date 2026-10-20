package agentui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Enter while a turn runs queues the message instead of dropping it: the
// queue renders dimmed above the input with its count, and flushes oldest
// first when the turn ends.
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

	// The block rides above the input box: a count header, then one muted
	// numbered line per message in send order.
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "2 queued \u00b7 sent when this turn ends") ||
		!strings.Contains(view, "1 \u2981 first") || !strings.Contains(view, "2 \u2981 second") {
		t.Fatalf("queue block missing:\n%s", view)
	}

	// One queued message per turn end, oldest first, so each response can
	// react to the message before it.
	for _, want := range []string{"first", "second"} {
		_, cmd := m.Update(responseMsg{text: "ok"})
		if cmd == nil {
			t.Fatalf("turn end with a queued message must send, got nil cmd")
		}
		// The send is a command the caller runs, so what shows here is the queued
		// text landing as that turn's user line, and the queue holding the rest.
		last := m.chatLog.Last()
		if last == nil {
			t.Fatalf("no user line, want %q", want)
		}
		if last.role != "user" || last.text != want {
			t.Fatalf("last message = %q, want user %q", last.text, want)
		}
	}
	// The queued send leaves a turn in flight and the input untouched.
	if !m.sending || m.input.Value() != "" || len(m.queued) != 0 {
		t.Fatalf("sending = %v, input = %q, queued = %#v", m.sending, m.input.Value(), m.queued)
	}
}

// Up-arrow on an empty input pulls the newest queued message back for
// editing, and it leaves the queue, so Enter re-queues the one edit instead
// of sending a second copy of the original.
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
	// The pull hands the newest message back at the caret's end and drops it
	// from the queue, so it cannot be sent twice.
	if m.input.Value() != "second" || fmt.Sprintf("%v", m.queued) != "[first]" {
		t.Fatalf("input = %q, queued = %#v", m.input.Value(), m.queued)
	}
	// Enter re-queues the edited draft at the tail, after the older message.
	m.input.SetValue("second (edited)")
	m.updateKey(testKey(tea.KeyEnter))
	if fmt.Sprintf("%v", m.queued) != "[first second (edited)]" {
		t.Fatalf("queued = %#v, want [first second (edited)]", m.queued)
	}
	// The queue flushes in that order, one message per turn end.
	for _, want := range []string{"first", "second (edited)"} {
		m.Update(responseMsg{text: "ok"})
		// The send is a command the caller runs, so what shows here is the queued
		// text landing as that turn's user line, and the queue holding the rest.
		last := m.chatLog.Last()
		if last == nil {
			t.Fatalf("no user line, want %q", want)
		}
		if last.role != "user" || last.text != want {
			t.Fatalf("last message = %q, want user %q", last.text, want)
		}
	}
	// The flush sends queued text, so it never reads or clears the input: the
	// Enter that queued the edit already handed the input back empty.
	if len(m.queued) != 0 || m.input.Value() != "" {
		t.Fatalf("queued = %#v, input = %q", m.queued, m.input.Value())
	}
}

// Queuing leaves the interrupt alone: mid-turn ctrl+c still cancels the turn
// in flight and keeps the UI open, and it neither drops nor sends the queue.
func TestModel_QueuedMessagesLeaveInterruptUnchanged(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, &fakePromptHistory{}, nil)
	m.sending = true
	m.queued = []string{"later"}
	cancelled := false
	m.cancelSend = func() { cancelled = true }
	_, cmd := m.updateKey(testModifiedKey('c', tea.ModCtrl))
	if isQuit(cmd) || !cancelled || !m.stopping {
		// ctrl+c must not become a send or a quit while messages are queued.
		t.Fatalf("cancelled = %v, stopping = %v, quit = %v", cancelled, m.stopping, isQuit(cmd))
	}
	// The stop ends the turn, so the queued message flushes as the next turn;
	// the stop itself neither drops it nor sends it.
	if fmt.Sprintf("%v", m.queued) != "[later]" {
		t.Fatalf("queued = %#v, want [later]", m.queued)
	}
}
