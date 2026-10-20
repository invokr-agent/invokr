package agentui

import (
	"cmp"
	"context"
	"image/color"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/antonikliment/tuikit"
	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-ai/invokr/surface/ui"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/tool_search"
	"github.com/invokr-ai/invokr/tools/system/toolbox"
)

type toolDialogSource interface {
	ListTools(context.Context) ([]tool_search.Listing, error)
	SetToolEnabled(context.Context, string, bool) error
}

type toolDialogEntry struct {
	spec    core.ToolSpec
	listing tool_search.Listing
}

type toolDialog struct {
	entries  []toolDialogEntry
	list     tuikit.SelectList
	tab      int
	offset   int
	open     bool
	saving   bool
	feedback string
	// Install tab: hub listing, fetched on first visit, and per-tool approval overrides.
	hub       []toolbox.HubTool
	approvals map[string]string
	// suggest is a hub tool the dialog was opened to offer; it is listed first.
	suggest string
}

type toolToggleMsg struct {
	name    string
	enabled bool
	err     error
	// hubLoaded carries an Install-tab listing; installed reports an install of name.
	hub                  []toolbox.HubTool
	hubLoaded, installed bool
}

type hubToolsBackend interface {
	HubTools(context.Context) ([]toolbox.HubTool, error)
}

func (m *model) openToolDialog(specs []core.ToolSpec, listings []tool_search.Listing) {
	m.toolDialog = toolDialog{list: tuikit.NewSelectList(ui.Invokr.Palette), open: true, approvals: map[string]string{}, entries: toolDialogEntries(specs, listings)}
	m.renderToolDialog()
}

func toolDialogEntries(specs []core.ToolSpec, listings []tool_search.Listing) []toolDialogEntry {
	byName := make(map[string]tool_search.Listing, len(listings))
	for _, listing := range listings {
		byName[listing.Name] = listing
	}
	var entries []toolDialogEntry
	for _, spec := range specs {
		if listing, ok := byName[spec.Name]; ok {
			entries = append(entries, toolDialogEntry{spec: spec, listing: listing})
		}
	}
	slices.SortFunc(entries, func(a, b toolDialogEntry) int {
		return cmp.Or(cmp.Compare(toolDialogGroup(a), toolDialogGroup(b)), cmp.Compare(a.spec.Name, b.spec.Name))
	})
	return entries
}

// reloadToolEntries re-reads the live catalog so a tool installed from the Install tab shows under Custom tools.
func (m *model) reloadToolEntries() {
	source, specsOK := m.session.(toolSpecSource)
	manager, listOK := m.session.(toolDialogSource)
	if !specsOK || !listOK {
		return
	}
	if listings, err := manager.ListTools(m.ctx); err == nil {
		m.toolDialog.entries = toolDialogEntries(source.ToolSpecs(), listings)
	}
}

func toolDialogGroup(entry toolDialogEntry) string {
	source := cmp.Or(strings.TrimSpace(entry.spec.Metadata.Source), "native")
	if source == "native" {
		return "" // Native first.
	}
	return source
}

func (m *model) renderToolDialog() {
	d := &m.toolDialog
	keys, row := m.toolDialogRows()
	d.list.SetItems(core.MapSlice(keys, func(key string) tuikit.SelectItem { return tuikit.SelectItem{Key: key, Label: key} }))
	content := "no tools available"
	if d.tab == 2 && d.hub == nil {
		content = "fetching " + toolbox.HubRepo + "…"
	}
	if len(keys) > 0 {
		height := max(1, m.height-9)
		selected := slices.Index(keys, d.list.SelectedKey())
		d.offset = min(d.offset, selected)
		if selected >= d.offset+height {
			d.offset = selected - height + 1
		}
		lines := make([]string, 0, height)
		for i := d.offset; i < len(keys) && i < d.offset+height; i++ {
			lines = append(lines, row(i, i == selected))
		}
		content = strings.Join(lines, "\n")
	}
	hint := "tab switch · ↑↓ select · space/enter toggle · esc close"
	if d.tab == 2 {
		hint = "tab switch · ↑↓ select · a approval · enter install · esc close"
	}
	feedback := cmp.Or(d.feedback, hint)
	if d.saving {
		feedback = "saving…"
	}
	m.overlay.Open("tools", content+"\n"+feedback)
}

// toolDialogRows returns the current tab's row keys and a renderer for row i.
func (m *model) toolDialogRows() ([]string, func(int, bool) string) {
	d := &m.toolDialog
	width := min(m.overlayContentWidth(), 86)
	if d.tab == 2 {
		return core.MapSlice(d.hub, func(t toolbox.HubTool) string { return t.Name }), func(i int, selected bool) string {
			return hubToolRow(d.hub[i], d.approval(d.hub[i]), selected, width)
		}
	}
	var entries []toolDialogEntry
	for _, entry := range d.entries {
		if (toolDialogGroup(entry) == "") == (d.tab == 0) {
			entries = append(entries, entry)
		}
	}
	return core.MapSlice(entries, func(e toolDialogEntry) string { return e.spec.Name }), func(i int, selected bool) string {
		return toolDialogRow(entries[i], selected, width)
	}
}

func (d *toolDialog) approval(t toolbox.HubTool) string {
	return cmp.Or(d.approvals[t.Name], t.Approval)
}

func hubToolRow(t toolbox.HubTool, approval string, selected bool, width int) string {
	state, style := "[new]      ", ui.Invokr.Info
	if t.Installed {
		state, style = "[installed]", ui.Invokr.Success
	}
	marker := "  "
	if selected {
		marker = ui.Invokr.Accent.Render("› ")
	}
	line := marker + style.Render(state) + " " + ui.Invokr.Info.Bold(selected).Render(t.Name) + ui.Invokr.Muted.Render(" ("+approval+") — "+shortDescription(t.Description))
	return ansi.Truncate(line, max(1, width), "…")
}

func (m *model) toolDialogView(bg string) string {
	if m.width <= 0 || m.height <= 0 {
		return bg
	}
	palette := ui.Invokr.Palette
	return m.tabbedModal(bg, []string{"Core tools", "Custom tools", "Install"}, []color.Color{palette.Brand, palette.Blue, palette.Brand}, m.toolDialog.tab, min(m.width-2, 90), m.overlay.Content())
}

func toolDialogRow(entry toolDialogEntry, selected bool, width int) string {
	state, style := "[off]", ui.Invokr.Danger
	if entry.listing.Protected {
		state, style = "[always on]", ui.Invokr.Muted
	} else if entry.listing.Enabled {
		state, style = "[on ]", ui.Invokr.Success
	}
	marker := "  "
	if selected {
		marker = ui.Invokr.Accent.Render("› ")
	}
	line := marker + style.Render(state) + " " + ui.Invokr.Info.Bold(selected).Render(toolDisplayName(entry.spec))
	if source := toolDialogGroup(entry); source != "" {
		line += ui.Invokr.Muted.Render(" (" + source + ")")
	}
	line += ui.Invokr.Muted.Render(" — " + shortDescription(entry.spec.Description))
	return ansi.Truncate(line, max(1, width), "…")
}

func (m *model) updateToolDialogKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	d := &m.toolDialog
	if !d.open || !m.overlay.IsOpen() || m.overlay.Title() != "tools" {
		return nil, false
	}
	switch msg.String() {
	case "esc":
		m.overlay.Close()
		d.open = false
	case "up", "down":
		d.list.Update(msg)
		d.feedback = ""
	case "tab", "shift+tab", "left", "right":
		if cmd := m.switchToolTab(msg.String() == "shift+tab" || msg.String() == "left"); cmd != nil {
			return cmd, true
		}
	case "space", "enter":
		return m.activateToolRow(), true
	case "a":
		m.cycleHubApproval()
	default:
		return nil, true
	}
	if m.overlay.IsOpen() {
		m.renderToolDialog()
	}
	return nil, true
}

func (m *model) toggleSelectedTool() tea.Cmd {
	d := &m.toolDialog
	name := d.list.SelectedKey()
	for _, entry := range d.entries {
		if entry.spec.Name != name {
			continue
		}
		switch {
		case d.saving:
		case m.sending:
			d.feedback = "wait for the current turn to finish"
		case entry.listing.Protected:
			d.feedback = "core tool is always enabled"
		default:
			d.saving = true
			enabled := !entry.listing.Enabled
			m.renderToolDialog()
			return func() tea.Msg {
				return toolToggleMsg{name: name, enabled: enabled, err: m.session.(toolDialogSource).SetToolEnabled(m.ctx, name, enabled)}
			}
		}
		break
	}
	m.renderToolDialog()
	return nil
}

// switchToolTab moves between Core, Custom and Install; the first Install visit fetches the hub.
func (m *model) switchToolTab(back bool) tea.Cmd {
	d := &m.toolDialog
	step := map[bool]int{true: 2, false: 1}[back]
	d.tab, d.offset, d.feedback = (d.tab+step)%3, 0, ""
	d.list = tuikit.NewSelectList(ui.Invokr.Palette)
	if d.tab != 2 || d.hub != nil {
		return nil
	}
	m.renderToolDialog()
	return m.fetchHubTools()
}

func (m *model) activateToolRow() tea.Cmd {
	if m.toolDialog.tab == 2 {
		return m.installSelectedTool()
	}
	return m.toggleSelectedTool()
}

func (m *model) fetchHubTools() tea.Cmd {
	backend, ok := m.commands.(hubToolsBackend)
	if !ok {
		m.toolDialog.feedback = "toolbox hub unavailable in this runtime"
		return nil
	}
	return func() tea.Msg {
		found, err := backend.HubTools(m.ctx)
		return toolToggleMsg{hub: found, hubLoaded: true, err: err}
	}
}

func (m *model) cycleHubApproval() {
	d := &m.toolDialog
	if i := slices.IndexFunc(d.hub, func(t toolbox.HubTool) bool { return t.Name == d.list.SelectedKey() }); d.tab == 2 && i >= 0 {
		next := (slices.Index(toolbox.HubApprovals, d.approval(d.hub[i])) + 1) % len(toolbox.HubApprovals)
		d.approvals[d.hub[i].Name] = toolbox.HubApprovals[next]
	}
}

func (m *model) installSelectedTool() tea.Cmd {
	d := &m.toolDialog
	i := slices.IndexFunc(d.hub, func(t toolbox.HubTool) bool { return t.Name == d.list.SelectedKey() })
	if i < 0 || d.saving {
		return nil
	}
	d.saving = true
	m.renderToolDialog()
	name, approval := d.hub[i].Name, d.approval(d.hub[i])
	return func() tea.Msg {
		_, err := m.commands.Execute(m.ctx, "toolbox", []string{"install", name, approval})
		return toolToggleMsg{name: name, installed: true, err: err}
	}
}

func (m *model) updateToolToggle(msg toolToggleMsg) {
	d := &m.toolDialog
	d.saving = false
	switch {
	case msg.hubLoaded && msg.err == nil:
		d.hub = append([]toolbox.HubTool{}, msg.hub...)
		if i := slices.IndexFunc(d.hub, func(t toolbox.HubTool) bool { return t.Name == d.suggest }); i > 0 {
			d.hub = slices.Insert(slices.Delete(d.hub, i, i+1), 0, msg.hub[i])
		} else if i < 0 {
			d.feedback = ""
		}
	case msg.installed && msg.err == nil:
		for i := range d.hub {
			d.hub[i].Installed = d.hub[i].Installed || d.hub[i].Name == msg.name
		}
		m.reloadToolEntries()
		d.feedback = msg.name + " installed — now under Custom tools"
	case msg.err != nil:
		d.feedback = "error: " + msg.err.Error()
	default:
		m.applyToolToggle(msg)
	}
	if m.overlay.IsOpen() && m.overlay.Title() == "tools" {
		m.renderToolDialog()
	}
}

func (m *model) applyToolToggle(msg toolToggleMsg) {
	d := &m.toolDialog
	for i := range d.entries {
		if d.entries[i].spec.Name == msg.name {
			d.entries[i].listing.Enabled = msg.enabled
			break
		}
	}
	d.feedback = msg.name + " enabled"
	if !msg.enabled {
		d.feedback = msg.name + " disabled"
	}
}
