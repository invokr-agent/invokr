package agentui

import (
	"strconv"

	"github.com/antonikliment/tuikit"
)

// transcriptItem adapts Invokr's messages to tuikit's windowed list.
type transcriptItem struct {
	id     string
	rev    int
	render func(int) string
}

func (i transcriptItem) ID() string              { return i.id }
func (i transcriptItem) Revision() int           { return i.rev }
func (i transcriptItem) Render(width int) string { return i.render(width) }

func messageItemID(id uint64) string { return "message:" + strconv.FormatUint(id, 10) }

func (m *model) transcriptItems() []tuikit.ListItem {
	items := make([]tuikit.ListItem, 0, len(m.chatLog.messages)+2)
	items = append(items, transcriptItem{id: "intro", rev: boolRev(relayRunning.Load()), render: func(width int) string {
		return renderIntro(width, m.height)
	}})
	for idx := range m.chatLog.messages {
		msg := &m.chatLog.messages[idx]
		id := messageItemID(msg.id)
		if msg.live() {
			m.chat.Invalidate(id)
		}
		rev := int(msg.version) * 2
		if idx == m.focusedMsg() {
			rev++
		}
		index := idx
		items = append(items, transcriptItem{id: id, rev: rev, render: func(width int) string {
			rendered := m.chatLog.renderMessage(index, width, m.spinner.View())
			if index == m.focusedMsg() {
				rendered = markFocused(rendered)
			}
			return "\n" + rendered + "\n"
		}})
	}
	m.chat.Invalidate("intro")
	m.chat.Invalidate("tail")
	items = append(items, transcriptItem{id: "tail", render: func(int) string { return "\n" + m.chatTail() + "\n" }})
	return items
}

func boolRev(b bool) int {
	if b {
		return 1
	}
	return 0
}
