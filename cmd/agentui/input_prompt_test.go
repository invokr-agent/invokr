package agentui

import (
	"github.com/invokr-agent/invokr/capability/approval"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// promptRows reports which rendered input rows carry the caret.
func promptRows(t *testing.T, m *model) []int {
	t.Helper()
	var rows []int
	for i, line := range strings.Split(ansi.Strip(m.input.View()), "\n") {
		if strings.HasPrefix(line, "›") {
			rows = append(rows, i)
		}
	}
	return rows
}

func TestYOLOBannerPinnedAndCannotCycle(t *testing.T) {
	m := newTestModelWithLogs()
	m.policy = approval.NewPolicy(approval.YOLO, false)
	for _, view := range []tuiMode{modeChat, modeLogs} {
		m.logPanel.mode = view
		if text := ansi.Strip(m.View().Content); !strings.HasPrefix(text, "YOLO:") {
			t.Fatalf("warning not pinned: %q", text)
		}
	}
	m.logPanel.mode = modeChat
	m.updateKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m.setApprovalMode([]string{"manual"})
	if m.policy.Mode() != approval.YOLO {
		t.Fatal("YOLO left without restart")
	}
}

func TestInputPromptOnlyOnCursorRow(t *testing.T) {
	m := newTestModelWithLogs()
	m.input.SetValue("one\ntwo\nthree")
	m.input.MoveToEnd()
	if got := promptRows(t, m); len(got) != 1 || got[0] != 2 {
		t.Fatalf("caret rows = %v, want [2]\n%s", got, ansi.Strip(m.input.View()))
	}
	m.input.CursorUp()
	if got := promptRows(t, m); len(got) != 1 || got[0] != 1 {
		t.Fatalf("caret rows after cursor up = %v, want [1]", got)
	}
}

func TestInputPromptFollowsSoftWrap(t *testing.T) {
	m := newTestModelWithLogs()
	m.input.SetWidth(20)
	// The first logical line wraps into three display rows, so the caret on the
	// second logical line belongs on display row 3, not on logical line 1.
	m.input.SetValue(strings.Repeat("wrapme ", 5) + "\nsecond")
	m.input.MoveToEnd()
	if got := promptRows(t, m); len(got) != 1 || got[0] != 3 {
		t.Fatalf("caret rows = %v, want [3]\n%s", got, ansi.Strip(m.input.View()))
	}
}

func TestNewlineAliases(t *testing.T) {
	for _, keys := range []tea.KeyPressMsg{
		{Code: tea.KeyEnter, Mod: tea.ModAlt},
		{Code: tea.KeyEnter, Mod: tea.ModShift},
		{Code: 'j', Mod: tea.ModCtrl},
	} {
		m := newTestModelWithLogs()
		m.logPanel.mode = modeChat
		m.input.SetValue("one")
		m.input.MoveToEnd()
		m.updateKey(keys)
		if got := m.input.Value(); got != "one\n" {
			t.Fatalf("%v: input = %q, want %q", keys, got, "one\n")
		}
	}
}

func TestBackslashEnterContinuesLine(t *testing.T) {
	m := newTestModelWithLogs()
	m.logPanel.mode = modeChat
	m.input.SetValue("one\\")
	m.input.MoveToEnd()
	m.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.input.Value(); got != "one\n" {
		t.Fatalf("input = %q, want %q", got, "one\n")
	}
	if len(m.session.(*fakeSession).calls) != 0 {
		t.Fatalf("backslash continuation sent the message")
	}
}

func TestCtrlDQuitsOnEmptyLineOnly(t *testing.T) {
	m := newTestModelWithLogs()
	m.logPanel.mode = modeChat
	m.input.SetValue("text")
	m.input.MoveToEnd()
	m.updateKey(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !m.quitArmed.IsZero() {
		t.Fatalf("ctrl+d armed quit with text in the input")
	}
	m.input.SetValue("")
	m.updateKey(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m.quitArmed.IsZero() {
		t.Fatalf("ctrl+d on an empty line did not arm quit")
	}
}

func TestShiftTabCyclesApprovalMode(t *testing.T) {
	m := newTestModelWithLogs()
	m.logPanel.mode = modeChat
	shiftTab := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	m.updateKey(shiftTab)
	if m.policy.Mode() != "auto" {
		t.Fatalf("mode = %q, want auto", m.policy.Mode())
	}
	if got := ansi.Strip(m.statusRow()); !strings.Contains(got, "auto") {
		t.Fatalf("status row missing the mode cell: %q", got)
	}
	m.updateKey(shiftTab)
	if m.policy.Mode() != "plan" {
		t.Fatalf("mode = %q, want plan", m.policy.Mode())
	}
	m.updateKey(shiftTab)
	if m.policy.Mode() != "manual" {
		t.Fatalf("mode = %q, want manual", m.policy.Mode())
	}
	if got := ansi.Strip(m.statusRow()); !strings.Contains(got, "manual") {
		t.Fatalf("status row missing manual mode: %q", got)
	}
}
