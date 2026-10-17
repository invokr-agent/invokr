package agentui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/invokr-ai/invokr/surface/ui"
)

// Transcript folds: every settled tool block can collapse (its renderer's
// Summary — the preview plus a "▸ N more lines" indicator) or expand (Detail —
// the full recorded result). alt+x enters an explicit transcript-focus mode
// that moves a cursor across tool blocks; enter/space toggles the one under
// it, esc returns to the prompt. A mouse click on a block's header line
// toggles it directly, using MemoList's visible row lookup.

// focusedMsg is the message index the transcript-focus cursor sits on, or -1
// when focus mode is off.
func (m *model) focusedMsg() int {
	if !m.focusMode {
		return -1
	}
	return m.focusMsg
}

// updateFocusKey owns alt+x and, while focus mode is on, the fold keys. It
// reports whether it consumed the keypress; anything it does not claim falls
// through to the normal handling (scrolling still works, typing exits).
func (m *model) updateFocusKey(msg tea.KeyPressMsg) bool {
	if !m.focusMode {
		if key.Matches(msg, m.keys.FocusTranscript) {
			m.enterFocusMode()
			return true
		}
		return false
	}
	switch {
	case key.Matches(msg, m.keys.FocusTranscript), key.Matches(msg, m.keys.Interrupt):
		m.focusMode = false
	case key.Matches(msg, m.keys.FoldPrev):
		m.moveFocus(-1)
	case key.Matches(msg, m.keys.FoldNext):
		m.moveFocus(1)
	case key.Matches(msg, m.keys.FoldToggle):
		m.toggleFold(m.focusMsg)
	default:
		return false
	}
	m.syncChatViewport(false)
	m.scrollFocusedIntoView()
	return true
}

func (m *model) enterFocusMode() {
	last := -1
	for i := range m.chatLog.messages {
		if m.chatLog.messages[i].foldable() {
			last = i
		}
	}
	if last < 0 {
		m.commandMessage("no details to expand yet")
		m.syncChatViewport(true)
		return
	}
	m.focusMode = true
	m.focusMsg = last
	m.syncChatViewport(false)
	m.scrollFocusedIntoView()
}

// moveFocus steps the cursor to the next/previous foldable block, staying put
// at either end.
func (m *model) moveFocus(delta int) {
	for i := m.focusMsg + delta; i >= 0 && i < len(m.chatLog.messages); i += delta {
		if m.chatLog.messages[i].foldable() {
			m.focusMsg = i
			return
		}
	}
}

// toggleFold flips one block's fold state. The state lives on the message, so
// it survives streaming updates (blocks mutate in place, keyed by tool ID).
func (m *model) toggleFold(msgIdx int) {
	if msgIdx >= 0 && msgIdx < len(m.chatLog.messages) && m.chatLog.messages[msgIdx].foldable() {
		msg := &m.chatLog.messages[msgIdx]
		msg.expanded = !msg.expanded
		msg.version++
	}
}

// scrollFocusedIntoView keeps the focused block on screen after a move or a
// toggle changed the layout.
func (m *model) scrollFocusedIntoView() {
	if m.focusMsg >= 0 && m.focusMsg < len(m.chatLog.messages) {
		m.chat.EnsureVisible(messageItemID(m.chatLog.messages[m.focusMsg].id))
		m.userScrolled = !m.chat.Following()
	}
}

// clickFold toggles the block whose header line sits under a mouse click,
// reporting whether the click hit one. y is the screen row of the click.
func (m *model) clickFold(y int) bool {
	tabLines := 0
	if banner := m.modeBanner(); banner != "" {
		tabLines = lipgloss.Height(banner)
	}
	if tabs := m.tabStrip(); tabs != "" {
		tabLines += lipgloss.Height(tabs)
	}
	id, line, ok := m.chat.ItemAt(y - tabLines)
	if !ok || line != 1 { // message item starts with one spacer row
		return false
	}
	for i := range m.chatLog.messages {
		if messageItemID(m.chatLog.messages[i].id) != id || !m.chatLog.messages[i].foldable() {
			continue
		}
		m.toggleFold(i)
		if m.focusMode {
			m.focusMsg = i
		}
		m.syncChatViewport(false)
		return true
	}
	return false
}

// focusHint is the footer's key legend while transcript focus is on.
func focusHint(keys keyMap) string {
	return ui.Invokr.Warn.Render("BROWSE DETAILS") + "  " + ui.Invokr.Muted.Render("↑/↓ select · enter/space expand/collapse · esc/"+keys.FocusTranscript.Help().Key+" back to prompt")
}
