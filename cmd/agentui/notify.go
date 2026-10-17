package agentui

import (
	tea "charm.land/bubbletea/v2"
)

// notifyMode is how loudly the TUI calls for attention when it needs the user
// back: an approval arriving in an empty queue, or a turn finishing. Signals
// are emitted from inside the Bubble Tea loop (the existing event-handling
// sites), never from bus callbacks, and never in plain/exec mode — the plain
// renderer never constructs a notifier.
type notifyMode int

const (
	notifyOff notifyMode = iota
	notifyBell
	// notifyDesktop escalates: bell, plus an OSC 9 desktop notification, plus
	// a "● " window-title marker while approvals pend.
	notifyDesktop
)

var notifyModeNames = []string{"off", "bell", "desktop"}

func (n notifyMode) String() string { return notifyModeNames[n] }

// parseNotifyMode reads a stored or typed mode. Empty is the default (off),
// which is what prefs files written before the feature hold.
func parseNotifyMode(name string) (notifyMode, bool) {
	return parseMode[notifyMode](notifyModeNames, name)
}

// Terminal escape sequences. The title marker uses the XTWINOPS title stack
// (push before marking, pop to clear) so the user's original title is restored
// rather than overwritten with a guess.
const (
	seqBell      = "\a"
	seqTitlePush = "\x1b[22;0t"
	seqTitlePop  = "\x1b[23;0t"
)

func seqDesktopNotify(text string) string { return "\x1b]9;" + text + "\a" }
func seqTitleMark(text string) string     { return "\x1b]0;● " + text + "\a" }

// notifier decides which sequences a state change earns. Grant-covered
// requests are auto-decided before they ever reach the visible queue
// (approval.ApprovalQueue.autoDecision), so they can never trigger a signal
// here; a burst of N requests is one 0->N transition of the visible count, so
// it coalesces into one signal.
type notifier struct {
	mode notifyMode
	// titleMarked tracks whether the "● " marker is on the window title, so
	// the pop is emitted exactly once on drain and on exit.
	titleMarked bool
}

func (n *notifier) approvalsPending(prev, count int) tea.Cmd {
	if count == 0 {
		return n.clearTitle()
	}
	if prev > 0 || n.mode == notifyOff {
		return nil
	}
	seq := seqBell
	if n.mode == notifyDesktop {
		seq += seqDesktopNotify("invokr: approval required")
		if !n.titleMarked {
			n.titleMarked = true
			seq = seqTitlePush + seqTitleMark("invokr") + seq
		}
	}
	return rawSeq(seq)
}

func (n *notifier) turnDone() tea.Cmd {
	if n.mode == notifyOff {
		return nil
	}
	seq := seqBell
	if n.mode == notifyDesktop {
		seq += seqDesktopNotify("invokr: turn finished")
	}
	return rawSeq(seq)
}

func (n *notifier) clearTitle() tea.Cmd {
	if !n.titleMarked {
		return nil
	}
	n.titleMarked = false
	return rawSeq(seqTitlePop)
}

// exitSeq is the sequence a quit must flush so the marker never outlives the
// program; empty when nothing is marked.
func (n *notifier) exitSeq() string {
	if !n.titleMarked {
		return ""
	}
	n.titleMarked = false
	return seqTitlePop
}

func rawSeq(seq string) tea.Cmd { return tea.Raw(seq) }

// notifyPendingCmd recomputes the visible pending count and hands the
// transition to the notifier. Call sites are the loop's existing
// event-handling paths: tool-event drains, spinner ticks, turn completion.
func (m *model) notifyPendingCmd() tea.Cmd {
	prev := m.lastPending
	m.lastPending = m.pendingApprovals()
	return m.notify.approvalsPending(prev, m.lastPending)
}

// quitCmd flushes the title marker before quitting so the terminal is left as
// it was found.
func (m *model) quitCmd() tea.Cmd {
	if seq := m.notify.exitSeq(); seq != "" {
		return tea.Sequence(rawSeq(seq), tea.Quit)
	}
	return tea.Quit
}

func (m *model) setNotifyMode(args []string) {
	const usage = "usage: /notify [off|bell|desktop]"
	if len(args) > 1 {
		m.commandMessage(usage)
		return
	}
	mode := (m.notify.mode + 1) % 3
	if len(args) == 1 {
		parsed, ok := parseNotifyMode(args[0])
		if !ok || args[0] == "" {
			m.commandMessage(usage)
			return
		}
		mode = parsed
	}
	m.notify.mode = mode
	m.savePrefs()
	m.commandMessage("notify: " + mode.String() + notifyModeHint(mode))
}

func notifyModeHint(mode notifyMode) string {
	switch mode {
	case notifyBell:
		return " (terminal bell on approvals and finished turns)"
	case notifyDesktop:
		return " (bell + desktop notification + title marker)"
	default:
		return ""
	}
}
