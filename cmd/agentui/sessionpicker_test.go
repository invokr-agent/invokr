package agentui

import (
	"strings"
	"testing"
	"time"

	"github.com/antonikliment/tuikit"
	"github.com/invokr-agent/invokr/capability/sessionstore"
	"github.com/invokr-agent/invokr/surface/ui"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
)

func testPicker(t *testing.T, now time.Time, sessions []sessionstore.SessionInfo) pickerModel {
	t.Helper()
	list := tuikit.NewSelectList(ui.Invokr.Palette)
	list.SetItems(sessionItems(sessions, now))
	return pickerModel{
		list: list, sessions: sessionsByID(sessions),
		now: func() time.Time { return now }, width: 96, height: 24,
	}
}

func TestSessionAgeStaysShortPastTheDayBoundary(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-42 * time.Minute), "42m ago"},
		{now.Add(-2*time.Hour - 30*time.Minute), "2h ago"},
		// tuikit.Age gives up here and prints a full RFC 3339 timestamp.
		{now.Add(-25 * time.Hour), "Aug 7"},
		{now.AddDate(-1, 0, 0), "Aug 8 2025"},
	} {
		if got := sessionAge(tc.at, now); got != tc.want {
			t.Fatalf("sessionAge(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
}

func TestPickerGroupsByDateAndShowsLastMessage(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	sessions := []sessionstore.SessionInfo{
		{ID: "dea1702e-aaaa", Title: "Simple greeting exchange", Messages: 28,
			UpdatedAt: now.Add(-2 * time.Hour), Last: "try unsetting TOKF_HOME"},
		{ID: "802342f7-bbbb", Title: "Explanation of binary search tree", Messages: 6,
			UpdatedAt: now.Add(-26 * time.Hour), Last: "rotations keep it balanced"},
		{ID: "11112222-cccc", Title: "Older still", Messages: 3,
			UpdatedAt: now.Add(-72 * time.Hour)},
	}
	view := ansi.Strip(testPicker(t, now, sessions).listView(96, 40))

	for _, want := range []string{"TODAY", "YESTERDAY", "AUG 5"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker missing the %q group header:\n%s", want, view)
		}
	}
	if !strings.Contains(view, `last: "try unsetting TOKF_HOME"`) {
		t.Fatalf("picker missing the last-message detail line:\n%s", view)
	}
	if !strings.Contains(view, "2h ago") || strings.Contains(view, "T12:00:00Z") {
		t.Fatalf("picker did not use short relative ages:\n%s", view)
	}
	// A session with no last message keeps its title row and skips the detail.
	if strings.Count(view, "last: ") != 2 {
		t.Fatalf("detail line count = %d, want 2:\n%s", strings.Count(view, "last: "), view)
	}
	// The cursor starts on the most recent session.
	if !strings.Contains(view, "› Simple greeting exchange") {
		t.Fatalf("picker did not mark the selected row:\n%s", view)
	}
	assertLinesWithinWidth(t, testPicker(t, now, sessions).listView(96, 40), 96)
}

func TestPickerWindowKeepsTheCursorVisible(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	sessions := make([]sessionstore.SessionInfo, 0, 30)
	for i := range 30 {
		sessions = append(sessions, sessionstore.SessionInfo{
			ID: strings.Repeat("a", 7) + string(rune('a'+i)), Title: "session", Messages: i,
			UpdatedAt: now.Add(-time.Duration(i) * time.Hour), Last: "detail",
		})
	}
	picker := testPicker(t, now, sessions)
	for range 25 {
		picker.list.Update(testKey(tea.KeyDown))
	}
	view := picker.listView(96, 10)
	if got := len(strings.Split(view, "\n")); got != 10 {
		t.Fatalf("windowed view height = %d, want 10", got)
	}
	if !strings.Contains(ansi.Strip(view), "›") {
		t.Fatalf("cursor scrolled out of the window:\n%s", ansi.Strip(view))
	}
}

func TestPickerRowsShowTheDirectoryColumn(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	sessions := []sessionstore.SessionInfo{
		{ID: "dea1702e-aaaa", Title: "Scoped", Messages: 2, UpdatedAt: now, Cwd: "/home/me/work/mercury"},
		{ID: "802342f7-bbbb", Title: "Legacy", Messages: 2, UpdatedAt: now},
	}
	picker := testPicker(t, now, sessions)
	picker.tab = tabAll
	view := ansi.Strip(picker.listView(96, 40))
	if !strings.Contains(view, "/home/me/work/mercury") {
		t.Fatalf("picker row missing its directory column:\n%s", view)
	}
	picker.tab = tabCwd
	if strings.Contains(ansi.Strip(picker.listView(96, 40)), "/home/me/work") {
		t.Fatalf("the directory column belongs to the All tab only:\n%s", view)
	}
}

func TestPickerCtrlCLeavesWithoutChoosing(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	m := testPicker(t, now, []sessionstore.SessionInfo{{ID: "dea1702e-aaaa", Title: "One", UpdatedAt: now}})
	updated, cmd := m.updateKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatalf("ctrl+c should quit the picker")
	}
	if picker, ok := updated.(pickerModel); !ok || picker.chosen != "" {
		t.Fatalf("ctrl+c must not choose a session, got %#v", updated)
	}
}
