package agentui

import (
	"context"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/capability/memory/history"
	"github.com/invokr-ai/invokr/platform/turn"

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
