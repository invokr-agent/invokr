package agentui

import (
	"time"
	"unicode/utf8"

	"github.com/invokr-ai/invokr/surface/ui"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/antonikliment/tuikit"
)

// mouseMode is how much of the mouse the TUI takes from the terminal. Taking
// any of it costs the terminal's own text selection, which is why "off" exists
// and why "on" draws a selection of its own.
type mouseMode int

const (
	// mouseOn reports clicks, drags and the wheel: the transcript scrolls and a
	// drag selects. It is the zero value because it is the default.
	mouseOn mouseMode = iota
	// mouseOff leaves the mouse to the terminal — native selection, no wheel.
	mouseOff
	// mouseAll adds motion without a button held, for hover-driven UI.
	mouseAll
)

// mouseModeNames is the presets in value order: the /mouse argument, the
// stored preference, and what the command echoes back are all this list.
var mouseModeNames = []string{"on", "off", "all"}

func (m mouseMode) String() string { return mouseModeNames[m] }

// mouseModeTeaModes is the tracking mode the renderer writes per preset.
var mouseModeTeaModes = map[mouseMode]tea.MouseMode{
	mouseOn:  tea.MouseModeCellMotion,
	mouseOff: tea.MouseModeNone,
	mouseAll: tea.MouseModeAllMotion,
}

// teaMode is the tracking mode the renderer writes for this preset.
func (m mouseMode) teaMode() tea.MouseMode { return mouseModeTeaModes[m] }

// parseMouseMode reads a stored or typed preset. The empty string is the
// default rather than an error: that is what an old prefs file holds.
func parseMouseMode(name string) (mouseMode, bool) { return parseMode[mouseMode](mouseModeNames, name) }

// copyNoticeWindow is how long the footer says what was copied. It outlives a
// glance without outstaying the highlight it belongs to.
const copyNoticeWindow = 3 * time.Second

// copyNoticeExpiredMsg only exists to bring the render loop back once the
// notice is stale.
type copyNoticeExpiredMsg struct{}

// selectionStyle paints the drag highlight — the one place the design fills a
// background, the same one the pickers use for the cursor row.
func selectionStyle() lipgloss.Style { return lipgloss.NewStyle().Background(ui.Invokr.SelectedBg) }

// updateMouse tracks a left-button drag as a selection over the last rendered
// frame, and copies it on release. It reports whether it consumed the message.
func (m *model) updateMouse(msg tea.Msg) (tea.Cmd, bool) {
	if m.mouseMode == mouseOff {
		return nil, false
	}
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil, false
		}
		// A click on a tool block's header line toggles its fold; anywhere
		// else it anchors a selection.
		if m.clickFold(msg.Y) {
			return nil, true
		}
		m.selection.Begin(tuikit.Cell{Row: msg.Y, Col: msg.X})
		return nil, true
	case tea.MouseMotionMsg:
		if msg.Button != tea.MouseLeft {
			return nil, false
		}
		m.selection.Extend(tuikit.Cell{Row: msg.Y, Col: msg.X})
		return nil, true
	case tea.MouseReleaseMsg:
		// The highlight stays up after the button comes back: it is the only
		// feedback that the copy happened, and it is what the next click, a
		// keystroke, a scroll or new output takes down.
		text := m.selection.Text(m.frame)
		if text == "" {
			m.selection.Clear()
			return nil, false
		}
		m.copiedChars, m.copiedAt = utf8.RuneCountInString(text), time.Now()
		// Both clipboards: OSC 52 for ctrl+v, the primary one for the
		// middle-click paste that X11 users expect a selection to feed. The
		// tick is what repaints the footer when the notice expires — nothing
		// else is guaranteed to arrive by then.
		return tea.Batch(tea.SetClipboard(text), tea.SetPrimaryClipboard(text), tea.Tick(copyNoticeWindow, func(time.Time) tea.Msg { return copyNoticeExpiredMsg{} })), true
	}
	return nil, false
}

// setMouseMode is the /mouse command: no argument reports, a preset switches
// and is remembered.
func (m *model) setMouseMode(args []string) {
	const usage = "usage: /mouse [on|off|all]"
	if len(args) == 0 {
		m.commandMessage("mouse: " + m.mouseMode.String())
		return
	}
	if len(args) != 1 {
		m.commandMessage(usage)
		return
	}
	mode, ok := parseMouseMode(args[0])
	if !ok || args[0] == "" {
		m.commandMessage(usage)
		return
	}
	m.mouseMode = mode
	m.selection.Clear()
	m.savePrefs()
	m.commandMessage("mouse: " + mode.String() + mouseModeHint(mode))
}

func mouseModeHint(mode mouseMode) string {
	if mode == mouseOff {
		return " (terminal selection, no wheel scroll)"
	}
	return " (drag to select, release to copy)"
}
