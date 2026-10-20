package agentui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/invokr-ai/invokr/platform/events"
	toolcore "github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/shell"
)

// noteToolSuggestion records a shell denial that a toolbox tool would resolve,
// at most once per session, so updateToolEvents can offer the install.
func (m *model) noteToolSuggestion(ev events.Event) {
	if ev.Tool == nil || ev.Tool.Name != "shell" || m.toolSuggested[shell.GHToolboxTool] {
		return
	}
	if strings.Contains(ev.Tool.Output, shell.GHSandboxDenial) || strings.Contains(ev.Tool.Error, shell.GHSandboxDenial) {
		m.pendingToolSuggestion = shell.GHToolboxTool
	}
}

// openToolSuggestion opens /tools on the Install tab with name first in the
// list; enter installs it, esc dismisses. An open overlay or an already
// loaded tool leaves it unshown.
func (m *model) openToolSuggestion() tea.Cmd {
	name := m.pendingToolSuggestion
	m.pendingToolSuggestion = ""
	if name == "" || m.overlay.IsOpen() {
		return nil
	}
	if m.toolSuggested == nil {
		m.toolSuggested = map[string]bool{}
	}
	m.toolSuggested[name] = true
	if source, ok := m.session.(toolSpecSource); ok && slices.ContainsFunc(source.ToolSpecs(), func(s toolcore.ToolSpec) bool { return s.Name == name }) {
		return nil
	}
	runToolsCommand(m, "", nil)
	if !m.toolDialog.open {
		return nil
	}
	m.toolDialog.tab, m.toolDialog.suggest = 1, name
	cmd := m.switchToolTab(false)
	m.toolDialog.feedback = "gh can't log in inside the shell sandbox — enter installs " + name + " to open PRs · esc dismiss"
	m.renderToolDialog()
	return cmd
}
