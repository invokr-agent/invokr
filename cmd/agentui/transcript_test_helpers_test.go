package agentui

import "strings"

// chatContent renders the complete transcript for assertions that inspect
// messages outside the visible MemoList window.
func (m *model) chatContent() string {
	return renderIntro(m.mainWidth(), m.height) + "\n\n" +
		m.chatLog.content(m.mainWidth(), m.spinner.View(), m.focusedMsg()) + m.chatTail()
}

func (c *chatLog) Content(width int, spinnerFrame string) string {
	return c.content(width, spinnerFrame, -1)
}

func (c *chatLog) content(width int, spinnerFrame string, focused int) string {
	var b strings.Builder
	for i := range c.messages {
		rendered := c.renderMessage(i, width, spinnerFrame)
		if i == focused {
			rendered = markFocused(rendered)
		}
		b.WriteString(rendered + "\n\n")
	}
	return b.String()
}
