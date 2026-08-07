package agentui

import (
	"cmp"
	"image/color"
	"slices"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/antonikliment/tuikit/markdown"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/surface/ui"
)

// The settings panel gathers the per-setting slash commands into one tabbed
// popup. Each tab is a huh form; a field change goes straight through the
// setter its slash command uses, so the panel adds no setting logic of its own.
// Only the Setup tab defers: it writes config.json on Save.

var settingsTitles = []string{"View", "Session", "Profiles", "Setup"}

const (
	settingsHint   = "ctrl+←/→ tabs · tab fields · esc close"
	setupTab       = 3
	currentProfile = "(current)"
)

var reasoningLevels = []string{"default", "none", "minimal", "low", "medium", "high", "xhigh", "max"}

// settingField is one select bound to a string; applied is the value last
// handed to apply, so a change is applied once.
type settingField struct {
	value   *string
	applied string
	apply   func(*model, string)
}

type setupDraft struct {
	baseURL, model string
	memory, save   bool
}

type settingsPanel struct {
	open   bool
	tab    int
	forms  [4]*huh.Form
	fields [4][]*settingField
	models []string
	setup  setupDraft
}

func runSettingsCommand(m *model, _ string, _ []string) tea.Cmd { return m.openSettings() }

func settingsPlainText(turn.Session, []string) string {
	return "the settings panel is TUI-only; use /mode, /model, /reasoning, /profile, /progress, /notify, /mouse, /syntax"
}

func (m *model) openSettings() tea.Cmd {
	m.settings.open, m.settings.tab = true, 0
	cmds := make([]tea.Cmd, 0, len(settingsTitles)+1)
	for tab := range settingsTitles {
		cmds = append(cmds, m.buildSettingsTab(tab))
	}
	ctx, sess := m.ctx, m.session
	return tea.Batch(append(cmds, func() tea.Msg { return fetchModelList(ctx, sess) })...)
}

func (m *model) buildSettingsTab(tab int) tea.Cmd {
	m.settings.fields[tab] = nil
	build := [...]func(*model) []huh.Field{(*model).viewSettings, (*model).sessionSettings, (*model).profileSettings, (*model).setupSettings}[tab]
	form := huh.NewForm(huh.NewGroup(build(m)...)).WithShowHelp(false).WithTheme(huh.ThemeFunc(huh.ThemeCharm)).WithWidth(max(20, m.settingsWidth()-4))
	m.settings.forms[tab] = form
	return form.Init()
}

// updateSettingsKey opens the panel on ctrl+o and, while it is open, claims
// every key: the panel is modal like the popup.
func (m *model) updateSettingsKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if m.settings.open {
		return m.updateSettings(msg), true
	}
	if key.Matches(msg, m.keys.Settings) {
		return m.openSettings(), true
	}
	return nil, false
}

func (m *model) updateSettings(msg tea.Msg) tea.Cmd {
	p := &m.settings
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			p.open = false
			return nil
		case "ctrl+left", "ctrl+right":
			step := map[bool]int{true: 1, false: len(settingsTitles) - 1}[k.String() == "ctrl+right"]
			p.tab = (p.tab + step) % len(settingsTitles)
			return nil
		}
	}
	tab := p.tab
	_, cmd := p.forms[tab].Update(msg)
	for _, f := range p.fields[tab] {
		if *f.value != f.applied {
			f.applied = *f.value
			f.apply(m, f.applied)
			m.syncChatViewport(true)
		}
	}
	if state := p.forms[tab].State; state != huh.StateNormal {
		if tab == setupTab && state == huh.StateCompleted && p.setup.save {
			m.saveSetup()
		}
		// A finished huh form stops taking input, so it starts over.
		return tea.Batch(cmd, m.buildSettingsTab(tab))
	}
	return cmd
}

func (m *model) saveSetup() {
	s, path := m.settings.setup, config.DefaultConfigPath()
	if err := config.SetSetupFields(path, s.baseURL, s.model, s.memory); err != nil {
		m.commandMessage("settings: " + err.Error())
		return
	}
	m.commandMessage("setup saved to " + path + " (applies after restart)")
}

// choice is an inline select whose change is applied at once.
func (m *model) choice(tab int, title, current string, options []string, apply func(*model, string)) huh.Field {
	if !slices.Contains(options, current) {
		options = append([]string{current}, options...)
	}
	f := &settingField{value: new(current), applied: current, apply: apply}
	m.settings.fields[tab] = append(m.settings.fields[tab], f)
	return huh.NewSelect[string]().Title(title).Options(huh.NewOptions(options...)...).Value(f.value).Inline(true)
}

func argSetter(set func(*model, []string)) func(*model, string) {
	return func(m *model, v string) { set(m, []string{v}) }
}

func (m *model) viewSettings() []huh.Field {
	hints := map[bool]string{false: "show", true: "hide"}[m.hideHints]
	return []huh.Field{
		m.choice(0, "Mouse", m.mouseMode.String(), mouseModeNames, argSetter((*model).setMouseMode)),
		m.choice(0, "Notify", m.notify.mode.String(), notifyModeNames, argSetter((*model).setNotifyMode)),
		m.choice(0, "Tool progress", m.chatLog.toolProgress.String(), progressModeNames, argSetter((*model).setProgress)),
		m.choice(0, "Key hints", hints, []string{"show", "hide"}, func(m *model, v string) { m.setHideHints(v == "hide") }),
		m.choice(0, "Syntax theme", cmp.Or(syntaxTheme, autoSyntaxTheme), append([]string{autoSyntaxTheme}, markdown.SyntaxThemes()...), func(m *model, v string) {
			if v == autoSyntaxTheme {
				v = ""
			}
			m.applySyntaxTheme(v)
		}),
	}
}

func (m *model) sessionSettings() []huh.Field {
	modes := []string{"manual", "auto", "plan"}
	publishing := map[bool]string{false: "ask", true: "allow"}[m.policy.Publishing()]
	fields := []huh.Field{
		m.choice(1, "Model", currentModel(m.session), m.settings.models, func(m *model, v string) { runModelCommand(m, "/model "+v, nil) }),
		m.choice(1, "Mode", string(m.policy.Mode()), modes, argSetter((*model).setApprovalMode)),
		m.choice(1, "Auto publishing", publishing, []string{"ask", "allow"}, argSetter((*model).setPublishing)),
	}
	if rs, ok := m.session.(reasoningSession); ok {
		fields = append(fields, m.choice(1, "Reasoning", cmp.Or(rs.ReasoningEffort().Level, "default"), reasoningLevels, func(m *model, v string) {
			m.commandMessage(reasoningFeedback(m.session, []string{v}))
		}))
	}
	return fields
}

func (m *model) profileSettings() []huh.Field {
	home := config.InvokrHomeDir()
	names, err := config.ListProfiles(home)
	if err != nil || len(names) == 0 {
		return []huh.Field{huh.NewNote().Title("Profiles").Description(renderProfileList(home))}
	}
	return []huh.Field{m.choice(2, "Profile", currentProfile, names, func(m *model, v string) {
		if v != currentProfile {
			m.commandMessage(profileCommandText(m.session, []string{v}))
		}
	})}
}

func (m *model) setupSettings() []huh.Field {
	// The file's own values, not LoadFile's: that layers defaults and INVOKR_*
	// env on top, and Save would then write a temporary override to disk.
	cfg, _, _ := fsutil.ReadJSON[config.AppConfig](config.DefaultConfigPath())
	s := &m.settings.setup
	*s = setupDraft{baseURL: cfg.Provider.BaseURL, model: cfg.Provider.Model, memory: cfg.Memory.Zerfoo.Enabled}
	return []huh.Field{
		huh.NewInput().Title("Provider URL").Value(&s.baseURL),
		huh.NewInput().Title("Default model").Value(&s.model),
		huh.NewConfirm().Title("Memory (zerfoo)").Value(&s.memory),
		huh.NewNote().Title("Home").Description(config.InvokrHomeDir() + "\nApplies after restart; an active router profile's provider/model win at runtime."),
		huh.NewConfirm().Title("Write config.json?").Affirmative("Save").Negative("Cancel").Value(&s.save),
	}
}

func (m *model) settingsWidth() int { return min(m.width-2, 90) }

// settingsView composites the panel over the finished frame, the way the
// popup is drawn, so the transcript underneath keeps its layout.
func (m *model) settingsView(bg string) string {
	p := &m.settings
	if !p.open || m.width <= 0 || m.height <= 0 {
		return bg
	}
	// The hint leads the body so a short terminal clips the form, not the hint.
	body := ui.Invokr.Muted.Render(settingsHint) + "\n\n" + p.forms[p.tab].View()
	pal := ui.Invokr.Palette
	accents := []color.Color{pal.Brand, pal.Blue, pal.Cyan, pal.Yellow}
	panel := pal.TabbedPanel(settingsTitles, accents, p.tab, m.settingsWidth(), min(m.height-2, lipgloss.Height(body)+4), body)
	layers := lipgloss.NewCompositor(
		lipgloss.NewLayer(bg).Z(0),
		lipgloss.NewLayer(panel).X(max(0, (m.width-lipgloss.Width(panel))/2)).Y(max(0, (m.height-lipgloss.Height(panel))/2)).Z(1),
	)
	return lipgloss.NewCanvas(m.width, m.height).Compose(layers).Render()
}
