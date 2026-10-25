package agentui

import (
	"image/color"
	"strings"

	"github.com/invokr-ai/invokr/surface/ui"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type tuiMode uint8

const (
	modeChat tuiMode = iota
	modeLogs
)

type logPanel struct {
	source   LogSource
	mode     tuiMode
	viewport viewport.Model
}

func newLogPanel(source LogSource) logPanel {
	v := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	for _, binding := range []*key.Binding{&v.KeyMap.HalfPageUp, &v.KeyMap.HalfPageDown, &v.KeyMap.Left, &v.KeyMap.Right} {
		binding.Unbind()
	}
	return logPanel{source: source, viewport: v}
}

func (p logPanel) enabled() bool { return p.source != nil }

func (m *model) logsAvailable() bool { return m.logView && m.logPanel.enabled() }

// mainWidth is the usable width, inside the YOLO border when a YOLO session
// draws one: the frame costs a column on each side and everything else lays
// itself out against this, so a bordered session cannot overflow the terminal.
func (m *model) mainWidth() int { return m.width - m.frameInset() }

func (m *model) tabStrip() string {
	if !m.logsAvailable() {
		return ""
	}
	titles := []string{"Chat", "Logs"}
	if m.approvalQueue != nil && m.approvalQueue.Len() > 0 {
		titles[0] += " •"
	}
	accent := ui.Invokr.Palette.Brand
	return ui.Invokr.Palette.TabStrip(titles, []color.Color{accent, accent}, int(m.logPanel.mode))
}

func (m *model) logsView() string {
	m.syncLogViewport()
	content := m.logPanel.viewport.View()
	if strings.TrimSpace(content) == "" {
		content = "waiting for logs"
	}
	hint := ui.Invokr.Muted.Render("↑/↓ j/k pgup/pgdn home/end · wheel · tab switch view")
	return m.tabStrip() + "\n" + hint + "\n" + ui.Invokr.Muted.Render(content)
}

func (m *model) updateViewSwitchKey(msg tea.KeyPressMsg) bool {
	if !m.logsAvailable() || !key.Matches(msg, m.keys.SwitchView) {
		return false
	}
	if m.logPanel.mode == modeLogs {
		m.showChat()
	} else {
		m.showLogs()
	}
	return true
}

func (m *model) updateLogsModeKey(msg tea.KeyPressMsg) bool {
	if m.logPanel.mode != modeLogs {
		return false
	}
	if m.updateViewSwitchKey(msg) {
		return true
	}
	return m.updateLogsKey(msg)
}

func (m *model) updateLogsKey(msg tea.KeyPressMsg) bool {
	m.syncLogViewport()
	switch {
	case key.Matches(msg, m.keys.Home):
		m.logPanel.viewport.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.logPanel.viewport.GotoBottom()
	case key.Matches(msg, m.keys.QueueUp):
		m.logPanel.viewport.ScrollUp(1)
	case key.Matches(msg, m.keys.QueueDown):
		m.logPanel.viewport.ScrollDown(1)
	case key.Matches(msg, m.keys.Up, m.keys.Down, m.keys.PageUp, m.keys.PageDn):
		m.logPanel.viewport, _ = m.logPanel.viewport.Update(msg)
	}
	return true
}

func (m *model) showLogs() {
	if !m.logsAvailable() {
		return
	}
	m.logPanel.mode = modeLogs
	m.input.Blur()
	m.syncLogViewport()
}

func (m *model) showChat() {
	m.logPanel.mode = modeChat
	m.input.Focus()
	m.syncChatViewport(false)
}

func (m *model) syncLogViewport() {
	if !m.logsAvailable() {
		return
	}
	width := max(1, m.width)
	height := m.height - lipgloss.Height(m.tabStrip()) - 1
	if banner := m.modeBanner(); banner != "" {
		height -= lipgloss.Height(banner)
	}
	height = max(1, height)
	content := ansi.Wrap(strings.Join(m.logPanel.source.Lines(), "\n"), width, "")
	followViewport(&m.logPanel.viewport, width, height, content, false)
}
