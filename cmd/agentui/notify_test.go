package agentui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// rawOf executes a notifier command and returns the raw sequence it would
// write, or "" for a nil command.
func rawOf(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		return ""
	}
	msg, ok := cmd().(tea.RawMsg)
	if !ok {
		t.Fatalf("notifier command did not produce a RawMsg")
	}
	seq, ok := msg.Msg.(string)
	if !ok {
		t.Fatalf("raw message is not a string")
	}
	return seq
}

func TestNotifierApprovalTransitions(t *testing.T) {
	n := &notifier{mode: notifyBell}
	if seq := rawOf(t, n.approvalsPending(0, 3)); seq != seqBell {
		t.Fatalf("burst of 3 into an empty queue = %q, want one bell", seq)
	}
	// Already-pending queue growing must not re-signal (coalescing).
	if cmd := n.approvalsPending(3, 4); cmd != nil {
		t.Fatalf("growth of a non-empty queue signaled")
	}
	// Draining in bell mode has no title to clear.
	if cmd := n.approvalsPending(4, 0); cmd != nil {
		t.Fatalf("drain in bell mode emitted something")
	}
}

func TestNotifierDesktopTitleMarker(t *testing.T) {
	n := &notifier{mode: notifyDesktop}
	seq := rawOf(t, n.approvalsPending(0, 1))
	for _, want := range []string{seqBell, "\x1b]9;", seqTitlePush, "\x1b]0;● "} {
		if !strings.Contains(seq, want) {
			t.Fatalf("desktop approval signal missing %q: %q", want, seq)
		}
	}
	if !n.titleMarked {
		t.Fatal("title not marked")
	}
	if seq := rawOf(t, n.approvalsPending(1, 0)); seq != seqTitlePop {
		t.Fatalf("drain = %q, want title pop", seq)
	}
	if n.titleMarked {
		t.Fatal("title still marked after drain")
	}
	// Exit with a marked title must flush the pop exactly once.
	n.titleMarked = true
	if got := n.exitSeq(); got != seqTitlePop {
		t.Fatalf("exitSeq = %q", got)
	}
	if got := n.exitSeq(); got != "" {
		t.Fatalf("second exitSeq = %q, want empty", got)
	}
}

func TestNotifierOffEmitsNothing(t *testing.T) {
	n := &notifier{mode: notifyOff}
	if n.approvalsPending(0, 1) != nil || n.turnDone() != nil {
		t.Fatal("off mode emitted a signal")
	}
}

func TestNotifierTurnDone(t *testing.T) {
	n := &notifier{mode: notifyBell}
	if seq := rawOf(t, n.turnDone()); seq != seqBell {
		t.Fatalf("turnDone = %q, want bell", seq)
	}
	n.mode = notifyDesktop
	if seq := rawOf(t, n.turnDone()); !strings.Contains(seq, "\x1b]9;") {
		t.Fatalf("desktop turnDone missing OSC 9: %q", seq)
	}
}

func TestNotifyModeCyclesAndSets(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.setNotifyMode(nil)
	if m.notify.mode != notifyBell {
		t.Fatalf("cycle from off = %v, want bell", m.notify.mode)
	}
	m.setNotifyMode(nil)
	if m.notify.mode != notifyDesktop {
		t.Fatalf("second cycle = %v, want desktop", m.notify.mode)
	}
	m.setNotifyMode(nil)
	if m.notify.mode != notifyOff {
		t.Fatalf("third cycle = %v, want off", m.notify.mode)
	}
	m.setNotifyMode([]string{"desktop"})
	if m.notify.mode != notifyDesktop {
		t.Fatalf("explicit set = %v, want desktop", m.notify.mode)
	}
	m.setNotifyMode([]string{"loud"})
	if m.notify.mode != notifyDesktop {
		t.Fatalf("bad arg changed the mode to %v", m.notify.mode)
	}
}
