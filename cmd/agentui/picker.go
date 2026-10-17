package agentui

import (
	"cmp"
	"context"
	"strings"

	"github.com/antonikliment/tuikit"
	"github.com/antonikliment/tuikit/markdown"
	"github.com/invokr-ai/invokr/capability/skills/hub"
	"github.com/invokr-ai/invokr/surface/ui"
	toolcore "github.com/invokr-ai/invokr/tools/core"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type listPicker struct {
	list            tuikit.SelectList
	current, footer string
	open            bool
}

const listPickerHeight = 10

func (p *listPicker) show(items []tuikit.SelectItem, current, footer string) {
	p.list = tuikit.NewSelectList(ui.Invokr.Palette)
	p.list.SetItems(items)
	p.list.Focus() // type-to-filter from the moment it opens
	p.current, p.footer, p.open = current, footer, true
}

func (p *listPicker) dismiss() { p.open, p.list = false, tuikit.SelectList{} }

func (p *listPicker) updateKey(msg tea.KeyPressMsg, onChoose func(string)) bool {
	if !p.open {
		return false
	}
	switch msg.String() {
	case "esc":
		// Esc closes the overlay outright; the list's own Esc only clears the
		// filter, which would strand the user in an empty-query picker.
		p.dismiss()
	case "enter":
		name := p.list.SelectedKey()
		p.dismiss()
		onChoose(name)
	default:
		p.list.Update(msg)
	}
	return true
}

func (p *listPicker) view(width int) string {
	head := ui.Invokr.Accent.Render("filter ") + ui.Invokr.Text.Render(p.list.Query()) + "  " + ui.Invokr.Muted.Render(p.list.Counter())
	footer := ui.Invokr.Muted.Render(p.footer)
	return strings.Join([]string{head, p.list.View(width, listPickerHeight), footer}, "\n")
}

func (p *listPicker) showModels(models []string, current string) {
	p.show(modelItems(models, current), current, "enter switch · esc cancel")
}

func modelItems(models []string, current string) []tuikit.SelectItem {
	return toolcore.MapSlice(models, func(id string) tuikit.SelectItem {
		item := tuikit.SelectItem{Label: id}
		if id == current {
			item.Trailing, item.Marked = "active", true
		}
		return item
	})
}

func (m *model) updateModelPickerKey(msg tea.KeyPressMsg) bool {
	return m.modelPicker.updateKey(msg, func(name string) {
		if name != "" && name != m.modelPicker.current {
			switchModelOnSession(m.session, name)
			m.commandMessage("model set to " + name + " (applies from next turn)")
			m.syncChatViewport(true)
		}
	})
}

// autoSyntaxTheme is the first entry, and what an empty setting means: let the
// stylesheet follow the terminal background instead of pinning one.
const autoSyntaxTheme = "auto"

func (p *listPicker) showSyntax(current string) {
	p.show(syntaxItems(current), current, "enter apply · esc cancel")
}

func syntaxItems(current string) []tuikit.SelectItem {
	names := append([]string{autoSyntaxTheme}, markdown.SyntaxThemes()...)
	return toolcore.MapSlice(names, func(name string) tuikit.SelectItem {
		item := tuikit.SelectItem{Label: name}
		if name == autoSyntaxTheme {
			item.Trailing = "match the terminal"
		}
		if name == current || (current == "" && name == autoSyntaxTheme) {
			item.Trailing, item.Marked = "active", true
		}
		return item
	})
}

func (m *model) updateSyntaxPickerKey(msg tea.KeyPressMsg) bool {
	return m.syntaxPicker.updateKey(msg, func(name string) {
		if name == "" {
			return
		}
		if name == autoSyntaxTheme {
			name = ""
		}
		if name != m.syntaxPicker.current {
			m.applySyntaxTheme(name)
		}
	})
}

// applySyntaxTheme repaints the transcript in the chosen stylesheet and
// remembers it. The renderers are rebuilt rather than told about the change:
// the renderer captures its stylesheet, and the streamer caches what it has
// already drawn.
func (m *model) applySyntaxTheme(name string) {
	setSyntaxTheme(name)
	m.chat.InvalidateAll()
	m.savePrefs()
	m.commandMessage("syntax theme set to " + cmp.Or(name, autoSyntaxTheme))
	m.syncChatViewport(true)
}

// hubSkillsBackend is the optional backend capability behind the picker; the
// plain Execute path stays a single-method interface for every other command.
type hubSkillsBackend interface {
	HubSkills(context.Context) ([]hub.HubSkill, error)
}

// hubSkillsMsg carries the hub scan the picker opens with; loading happens in
// a tea.Cmd because a first use shallow-clones the hubs.
type hubSkillsMsg struct {
	skills []hub.HubSkill
	err    error
}

func (p *listPicker) showSkills(found []hub.HubSkill) {
	items := toolcore.MapSlice(found, func(hs hub.HubSkill) tuikit.SelectItem {
		item := tuikit.SelectItem{Key: hs.Name, Label: hs.Name + " — " + hs.Description}
		if hs.Installed {
			item.Trailing, item.Marked = "installed", true
		}
		return item
	})
	p.show(items, "", "enter install · esc cancel")
}

// updateSkillPickerKey handles keys the overlay owns while it is open. Unlike
// the model and syntax pickers, choosing here starts async work (a hub install
// hits git and the network), so it hands back a tea.Cmd as well.
func (m *model) updateSkillPickerKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	var chosen string
	handled := m.skillPicker.updateKey(msg, func(name string) { chosen = name })
	if !handled || chosen == "" {
		return nil, handled
	}
	m.commandMessage("installing skill " + chosen + "…")
	m.syncChatViewport(true)
	name := chosen
	return func() tea.Msg {
		result, err := m.commands.Execute(m.ctx, "skills", []string{"install", name})
		return commandResultMsg{text: result, err: err}
	}, true
}

func runSkillsCommand(m *model, text string, args []string) tea.Cmd {
	if len(args) != 1 || args[0] != "install" {
		return runBackendCommand(m, text, args)
	}
	backend, ok := m.commands.(hubSkillsBackend)
	if !ok {
		m.commandMessage("skill hubs unavailable in this runtime")
		return nil
	}
	m.commandMessage("fetching skill hubs…")
	return func() tea.Msg {
		found, err := backend.HubSkills(m.ctx)
		return hubSkillsMsg{skills: found, err: err}
	}
}

func (m *model) updateHubSkills(msg hubSkillsMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.err != nil:
		m.commandMessage("error: " + msg.err.Error())
	case len(msg.skills) == 0:
		m.commandMessage("no skills found in the configured hubs")
	default:
		m.skillPicker.showSkills(msg.skills)
	}
	m.syncChatViewport(true)
	return m, nil
}

func (p *listPicker) showPalette() { p.show(paletteItems(), "", "enter run · esc cancel") }

func paletteItems() []tuikit.SelectItem {
	return toolcore.MapSlice(commandRegistry, func(spec commandSpec) tuikit.SelectItem {
		item := tuikit.SelectItem{
			Key:   spec.name,
			Label: "/" + spec.name + " — " + spec.summary,
		}
		if commandTakesArgs(spec) {
			item.Trailing = "args…"
		}
		return item
	})
}

func commandTakesArgs(spec commandSpec) bool { return strings.Contains(spec.usage, " ") }

// updateFoldOrPaletteKey groups the transcript-focus and palette key routing
// into one seam for updateKey (which has a complexity budget to honor).
func (m *model) updateFoldOrPaletteKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if m.updateFocusKey(msg) {
		return true, nil
	}
	return m.updatePaletteKey(msg)
}

func (m *model) updatePaletteKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if !m.palette.open {
		if !key.Matches(msg, m.keys.Palette) {
			return false, nil
		}
		m.palette.showPalette()
		m.syncChatViewport(true)
		return true, nil
	}
	var cmd tea.Cmd
	consumed := m.palette.updateKey(msg, func(name string) {
		cmd = m.runPaletteCommand(name)
	})
	return consumed, cmd
}

func (m *model) runPaletteCommand(name string) tea.Cmd {
	spec := findCommand(name)
	if spec == nil {
		return nil
	}
	if commandTakesArgs(*spec) {
		// The command wants arguments: hand the user a prefilled prompt
		// instead of running it bare.
		m.input.SetValue("/" + name + " ")
		m.input.CursorEnd()
		m.completion.refresh(m.input.Value())
		m.syncChatViewport(true)
		return nil
	}
	// runCommand resets the input as if the command had been typed there; the
	// palette ran beside the prompt, so whatever draft was typed comes back.
	draft := m.input.Value()
	cmd, _ := m.runCommand("/" + name)
	m.input.SetValue(draft)
	m.input.CursorEnd()
	m.syncChatViewport(true)
	return cmd
}
