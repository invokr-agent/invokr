package agentui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	Submit, Newline, Quit, Help, SwitchView                  key.Binding
	PageUp, PageDn, Home, End, Up, Down                      key.Binding
	QueueUp, QueueDown, ExpandItem                           key.Binding
	ApproveItem, DenyItem, ApproveAll, DenyAll               key.Binding
	AlwaysAllow, AlwaysDeny                                  key.Binding
	ScrollUp, ScrollDown, ToggleHints, Interrupt             key.Binding
	Redraw, EOF, CycleMode                                   key.Binding
	FocusTranscript, FoldToggle, FoldPrev, FoldNext, Palette key.Binding
	Settings, Recorded                                       key.Binding
}

func defaultKeyMap(showViewSwitch bool) keyMap {
	// shift+tab belongs to the approval-mode cycle, the way the other agent
	// CLIs bind it, so the view switch keeps plain tab.
	switchView := key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch view"))
	switchView.SetEnabled(showViewSwitch)
	return keyMap{
		Submit: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		// Terminals disagree about which of these they can even deliver: only a
		// kitty-protocol/modifyOtherKeys terminal distinguishes shift+enter, and
		// Windows Terminal swallows alt+enter for its own fullscreen toggle.
		// ctrl+j (0x0A) reaches every terminal, and submit() also accepts a
		// trailing backslash. Bind them all; the help text names one.
		Newline:     key.NewBinding(key.WithKeys("alt+enter", "shift+enter", "ctrl+j"), key.WithHelp("alt+enter", "newline")),
		Quit:        key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "stop/quit")),
		Help:        key.NewBinding(key.WithKeys("ctrl+h"), key.WithHelp("ctrl+h", "help")),
		SwitchView:  switchView,
		PageUp:      key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDn:      key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		Home:        key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "top")),
		End:         key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "bottom")),
		Up:          key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "history/up")),
		Down:        key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "history/down")),
		QueueUp:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "prev item")),
		QueueDown:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next item")),
		ApproveItem: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "approve")),
		DenyItem:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "deny")),
		ApproveAll:  key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "approve all")),
		DenyAll:     key.NewBinding(key.WithKeys("D", "esc"), key.WithHelp("D/esc", "deny all")),
		ExpandItem:  key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "details")),
		// The design writes this "always allow <tool>". A standing grant is
		// always bound to an argument pattern (ADR 0008), so what "g" grants is
		// this call, not the tool at large — the help text says so.
		AlwaysAllow: key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "always allow this call")),
		// "x" mirrors "g": a standing denial, same scope/expiry/pattern shape,
		// so "always deny" is as durable as "always allow".
		AlwaysDeny: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "always deny this call")),
		ScrollUp:   key.NewBinding(key.WithKeys("shift+up"), key.WithHelp("shift+↑", "scroll up")),
		ScrollDown: key.NewBinding(key.WithKeys("shift+down"), key.WithHelp("shift+↓", "scroll down")),
		// ctrl+g rather than the more obvious ctrl+k: bubbles/textarea binds
		// ctrl+k to delete-after-cursor, and the app's keymap is checked first,
		// so taking it would silently remove an editing key from the prompt.
		ToggleHints: key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "toggle key hints")),
		// The thinking indicator and every running tool line offer "esc" as the
		// way out of a turn in flight; before this it was only ctrl+c.
		Interrupt: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "interrupt turn")),
		// alt+x and ctrl+o: free of both the app keymap and bubbles/textarea's
		// editing keys (unlike ctrl+t/f/b/n/p, which the textarea binds).
		FocusTranscript: key.NewBinding(key.WithKeys("alt+x"), key.WithHelp("alt+x", "browse details")),
		Settings:        key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "settings")),
		Recorded:        key.NewBinding(key.WithKeys("alt+r"), key.WithHelp("alt+r", "recorded calls")),
		FoldToggle:      key.NewBinding(key.WithKeys("enter", "space"), key.WithHelp("enter", "expand/collapse")),
		FoldPrev:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "prev block")),
		FoldNext:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next block")),
		// readline staples: ctrl+l redraws a garbled screen, and ctrl+d on an
		// empty line means end-of-input, i.e. quit.
		Redraw:    key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "redraw")),
		CycleMode: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "approval mode")),
		EOF:       key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "quit on empty line")),
		// ctrl+p shadows the textarea's emacs previous-line, which stays
		// reachable as plain ↑ — the palette is worth the well-known chord.
		Palette: key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "command palette")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	bindings := []key.Binding{k.Submit, k.Newline, k.Recorded, k.FocusTranscript, k.Help}
	if k.SwitchView.Enabled() {
		bindings = append(bindings, k.SwitchView)
	}
	return append(bindings, k.Quit)
}

func (k keyMap) FullHelp() [][]key.Binding {
	primary := []key.Binding{k.Submit, k.Newline, k.CycleMode, k.Quit, k.Help}
	if k.SwitchView.Enabled() {
		primary = append(primary, k.SwitchView)
	}
	return [][]key.Binding{
		primary, {k.PageUp, k.PageDn, k.Home, k.End, k.Up, k.Down}, {k.ScrollUp, k.ScrollDown, k.ToggleHints, k.Interrupt}, {k.FocusTranscript, k.Palette, k.Settings, k.Recorded},
	}
}
