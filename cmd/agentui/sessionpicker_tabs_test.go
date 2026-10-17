package agentui

import (
	"strings"
	"testing"
	"time"

	"github.com/antonikliment/tuikit"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/surface/ui"

	tea "charm.land/bubbletea/v2"
)

// tabbedPicker builds a picker over sessions from two directories plus one
// legacy session with no recorded cwd, opened on the current directory.
func tabbedPicker(t *testing.T, now time.Time) pickerModel {
	t.Helper()
	sessions := []sessionstore.SessionInfo{
		{ID: "mine-1", UpdatedAt: now, Messages: 2, Preview: "here", Cwd: "/work/alpha"},
		{ID: "theirs", UpdatedAt: now, Messages: 2, Preview: "elsewhere", Cwd: "/work/beta"},
		{ID: "legacy", UpdatedAt: now, Messages: 2, Preview: "before cwd existed"},
	}
	m := pickerModel{
		list: tuikit.NewSelectList(ui.Invokr.Palette), all: sessions, cwd: "/work/alpha",
		now: func() time.Time { return now }, width: 96, height: 24,
	}
	m.setTab(tabCwd)
	return m
}

func keyPress(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab, Text: s}
}

func rowKeys(m pickerModel) []string {
	keys := make([]string, 0, len(m.list.Filtered()))
	for _, item := range m.list.Filtered() {
		keys = append(keys, item.Key)
	}
	return keys
}

func TestPickerOpensOnCurrentDirectory(t *testing.T) {
	m := tabbedPicker(t, time.Now())
	if got := rowKeys(m); len(got) != 1 || got[0] != "mine-1" {
		t.Fatalf("rows = %v, want only this directory's session", got)
	}
}

func TestPickerTabRevealsEverySessionIncludingLegacy(t *testing.T) {
	m := tabbedPicker(t, time.Now())
	m.setTab(tabAll)
	got := rowKeys(m)
	if len(got) != 3 {
		t.Fatalf("rows = %v, want all three sessions", got)
	}
	m.setTab(tabCwd)
	if got := rowKeys(m); len(got) != 1 {
		t.Fatalf("rows = %v, want back to this directory only", got)
	}
}

func TestPickerTabKeyTogglesTheView(t *testing.T) {
	m := tabbedPicker(t, time.Now())
	next, _ := m.updateKey(keyPress("tab"))
	toggled, ok := next.(pickerModel)
	if !ok {
		t.Fatalf("updateKey returned %T, want pickerModel", next)
	}
	if toggled.tab != tabAll {
		t.Fatalf("tab = %d, want tabAll", toggled.tab)
	}
	if got := rowKeys(toggled); len(got) != 3 {
		t.Fatalf("rows after tab = %v, want all three", got)
	}
}

func TestPickerEmptyDirectoryExplainsItself(t *testing.T) {
	now := time.Now()
	m := tabbedPicker(t, now)
	m.cwd = "/work/gamma" // a directory with no sessions of its own
	m.setTab(tabCwd)
	body := m.listView(96, 12)
	if !strings.Contains(body, "no sessions in this directory") {
		t.Fatalf("listView() = %q, want the empty-directory hint", body)
	}
	m.setTab(tabAll)
	if body := m.listView(96, 12); strings.Contains(body, "no sessions in this directory") {
		t.Fatalf("the all tab must not claim the directory is empty: %q", body)
	}
}

func TestPickerTabStripCountsBothViews(t *testing.T) {
	m := tabbedPicker(t, time.Now())
	strip := m.tabStrip()
	if !strings.Contains(strip, "This directory (1)") || !strings.Contains(strip, "All (3)") {
		t.Fatalf("tabStrip() = %q, want both counts", strip)
	}
}
