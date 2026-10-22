package agentui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-ai/invokr/surface/ui"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/tool_search"
	"github.com/invokr-ai/invokr/tools/system/toolbox"
)

type dialogSession struct {
	liveCommandSession
	listings []tool_search.Listing
	setErr   error
	changes  []string
}

func (s *dialogSession) ListTools(context.Context) ([]tool_search.Listing, error) {
	return append([]tool_search.Listing(nil), s.listings...), nil
}

func (s *dialogSession) SetToolEnabled(_ context.Context, name string, enabled bool) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.changes = append(s.changes, name)
	for i := range s.listings {
		if s.listings[i].Name == name {
			s.listings[i].Enabled = enabled
		}
	}
	return nil
}

func TestToolDialogToggleAndProtectedRows(t *testing.T) {
	s := &dialogSession{
		liveCommandSession: liveCommandSession{specs: []core.ToolSpec{{Name: "read", Description: "Read files."}, {Name: "optional", Description: "Optional tool."}}},
		listings:           []tool_search.Listing{{Name: "read", Enabled: true, Protected: true}, {Name: "optional"}},
	}
	m := newModel(context.Background(), s, nil, nil, nil, false, nil, nil)
	m.width, m.height = 100, 30
	_, _ = m.runCommand("/tools")
	if got := ansi.Strip(m.overlay.Content()); !strings.Contains(got, "[off] optional") || !strings.Contains(got, "[always on] read") {
		t.Fatalf("dialog = %q", got)
	}
	_, _ = m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyDown})
	cmd, _ := m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || len(s.changes) != 0 || !strings.Contains(m.overlay.Content(), "core tool is always enabled") {
		t.Fatal("protected tool toggled")
	}
	_, _ = m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyUp})
	cmd, _ = m.updateToolDialogKey(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil || len(s.changes) != 0 {
		t.Fatal("toggle was not scheduled")
	}
	msg := cmd().(toolToggleMsg)
	m.updateToolToggle(msg)
	if msg.err != nil || len(s.changes) != 1 || !strings.Contains(ansi.Strip(m.overlay.Content()), "[on ] optional") {
		t.Fatalf("toggle result = %+v, changes = %v", msg, s.changes)
	}
	_, _ = m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.overlay.IsOpen() {
		t.Fatal("dialog stayed open")
	}
	_, _ = m.runCommand("/tools")
	if !strings.Contains(ansi.Strip(m.overlay.Content()), "[on ] optional") {
		t.Fatal("reopen lost saved state")
	}
}

func TestToolDialogBlocksRunningTurnAndKeepsStateOnError(t *testing.T) {
	s := &dialogSession{
		liveCommandSession: liveCommandSession{specs: []core.ToolSpec{{Name: "optional", Description: "Optional tool."}}},
		listings:           []tool_search.Listing{{Name: "optional"}},
	}
	m := newModel(context.Background(), s, nil, nil, nil, false, nil, nil)
	_, _ = m.runCommand("/tools")
	m.sending = true
	cmd, _ := m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || !strings.Contains(m.overlay.Content(), "wait for the current turn") {
		t.Fatal("toggle accepted during active turn")
	}
	m.sending = false
	s.setErr = errors.New("disk full")
	cmd, _ = m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("toggle was not scheduled")
	}
	m.updateToolToggle(cmd().(toolToggleMsg))
	if got := ansi.Strip(m.overlay.Content()); !strings.Contains(got, "error: disk full") || !strings.Contains(got, "[off] optional") {
		t.Fatalf("failed toggle changed state: %q", got)
	}
}

func TestToolDialogTabsAndColors(t *testing.T) {
	s := &dialogSession{
		liveCommandSession: liveCommandSession{specs: []core.ToolSpec{
			{Name: "read", Description: "Read files."},
			{Name: "custom", Description: "Custom action.", Metadata: core.ToolMetadata{Source: "local"}},
		}},
		listings: []tool_search.Listing{{Name: "read", Enabled: true}, {Name: "custom"}},
	}
	m := newModel(context.Background(), s, nil, nil, nil, false, nil, nil)
	m.width, m.height = 100, 30
	_, _ = m.runCommand("/tools")
	if got := ansi.Strip(m.View().Content); strings.Count(got, "┌") != 1 || !strings.Contains(got, "Core tools") || !strings.Contains(got, "Custom tools") {
		t.Fatalf("tabs are not the outer panel: %q", got)
	}
	if got := m.overlay.Content(); !strings.Contains(got, ui.Invokr.Success.Render("[on ]")) || !strings.Contains(got, ui.Invokr.Info.Bold(true).Render("read (deferred)")) || strings.Contains(ansi.Strip(got), "custom (local)") {
		t.Fatalf("native tab colors/content = %q", got)
	}
	_, _ = m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.overlay.Content(); !strings.Contains(got, ui.Invokr.Danger.Render("[off]")) || !strings.Contains(ansi.Strip(got), "custom (deferred) (local)") || strings.Contains(ansi.Strip(got), "read (deferred)") {
		t.Fatalf("custom tab colors/content = %q", got)
	}
}

func TestToolDialogInstallReloadsCustomTools(t *testing.T) {
	s := &dialogSession{
		liveCommandSession: liveCommandSession{specs: []core.ToolSpec{{Name: "read"}}},
		listings:           []tool_search.Listing{{Name: "read", Protected: true}},
	}
	m := newModel(context.Background(), s, nil, nil, nil, false, nil, nil)
	m.width, m.height = 100, 30
	_, _ = m.runCommand("/tools")
	// The install lands while the dialog is open; the live catalog now has it.
	s.specs = append(s.specs, core.ToolSpec{Name: "units", Metadata: core.ToolMetadata{Source: "toolbox"}})
	s.listings = append(s.listings, tool_search.Listing{Name: "units"})
	m.updateToolToggle(toolToggleMsg{name: "units", installed: true})
	_, _ = m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := ansi.Strip(m.overlay.Content()); !strings.Contains(got, "[off] units") {
		t.Fatalf("custom tab after install = %q", got)
	}
}

// The hub listing lands after openToolSuggestion wrote its hint, so the load
// keeps that hint while a suggestion is pending; esc drops the suggestion.
func TestToolDialogHubLoadKeepsSuggestionHint(t *testing.T) {
	s := &dialogSession{
		liveCommandSession: liveCommandSession{specs: []core.ToolSpec{{Name: "read", Description: "Read files."}}},
		listings:           []tool_search.Listing{{Name: "read", Enabled: true, Protected: true}},
	}
	m := newModel(context.Background(), s, nil, nil, nil, false, nil, nil)
	m.width, m.height = 100, 30
	_, _ = m.runCommand("/tools")
	m.toolDialog.tab, m.toolDialog.suggest = 2, "units"
	m.toolDialog.feedback = "enter installs units"
	m.updateToolToggle(toolToggleMsg{hub: []toolbox.HubTool{{Name: "other"}, {Name: "units"}}, hubLoaded: true})
	if m.toolDialog.feedback != "enter installs units" || m.toolDialog.hub[0].Name != "units" {
		t.Fatalf("hub load kept feedback %q, hub = %+v", m.toolDialog.feedback, m.toolDialog.hub)
	}
	_, _ = m.updateToolDialogKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.toolDialog.suggest != "" {
		t.Fatalf("esc left suggest = %q", m.toolDialog.suggest)
	}
}
