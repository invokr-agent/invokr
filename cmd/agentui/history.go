package agentui

import (
	"context"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/capability/memory/history"
	"github.com/invokr-agent/invokr/platform/turn"

	tea "charm.land/bubbletea/v2"
)

func loadHistory(ctx context.Context, promptHistory turn.PromptHistory) []string {
	recent, err := promptHistory.Recent(ctx, history.DefaultLimit)
	if err != nil {
		return nil
	}
	return recent
}

// updateHistoryKey walks prompt history with up/down, but only on a
// single-line input where those keys are unambiguous.
func (m *model) updateHistoryKey(msg tea.KeyPressMsg) bool {
	if m.input.LineCount() != 1 {
		return false
	}
	switch msg.Key().Code {
	case tea.KeyUp:
		m.historyUp()
	case tea.KeyDown:
		m.historyDown()
	default:
		return false
	}
	return true
}

func (m *model) persistHistoryCmd(text string) tea.Cmd {
	return func() tea.Msg {
		return historySavedMsg{err: m.historyDB.Append(m.ctx, text)}
	}
}

func (m *model) appendHistory(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	newHistory := append(slices.DeleteFunc(slices.Clone(m.history), func(item string) bool { return item == text }), text)
	if len(newHistory) > history.DefaultLimit {
		newHistory = newHistory[len(newHistory)-history.DefaultLimit:]
	}
	m.history = newHistory
}

func (m *model) historyUp() {
	// While a turn is in flight and nothing is typed, up pulls the newest
	// queued message back into the input so a mistake can be fixed before it is
	// sent. It leaves the queue, so Enter re-queues it in place instead of a
	// second copy being sent when the turn ends.
	if m.input.Value() == "" && len(m.queued) > 0 {
		// The run may already have taken it: then it is in the transcript, not
		// editable.
		if s, ok := m.session.(steerer); ok {
			if _, pending := s.Unsteer(); !pending {
				return
			}
		}
		m.input.SetValue(m.queued[len(m.queued)-1])
		m.input.CursorEnd()
		m.queued = m.queued[:len(m.queued)-1]
		return
	}
	if len(m.history) == 0 || m.sending {
		return
	}
	if !m.browsing {
		m.draft = m.input.Value()
		m.historyPos = len(m.history)
		m.browsing = true
	}
	if m.historyPos > 0 {
		m.historyPos--
	}
	m.input.SetValue(m.history[m.historyPos])
	m.input.CursorEnd()
}

func (m *model) historyDown() {
	if !m.browsing || m.sending {
		return
	}
	if m.historyPos < len(m.history) {
		m.historyPos++
	}
	if m.historyPos >= len(m.history) {
		m.input.SetValue(m.draft)
		m.input.CursorEnd()
		m.stopBrowsing()
		return
	}
	m.input.SetValue(m.history[m.historyPos])
	m.input.CursorEnd()
}
