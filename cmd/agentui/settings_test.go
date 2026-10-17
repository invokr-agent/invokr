package agentui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/setup"
)

func openSettingsModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("INVOKR_HOME", t.TempDir())
	m := newTestModelWithHiddenLogs()
	m.width, m.height = 120, 40
	m.prefsPath = filepath.Join(t.TempDir(), prefsFile)
	m.Update(testModifiedKey('o', tea.ModCtrl))
	if !m.settings.open {
		t.Fatal("ctrl+o did not open the settings panel")
	}
	return m
}

func TestSettingsOpenCloseAndTabs(t *testing.T) {
	m := openSettingsModel(t)
	m.Update(testModifiedKey(tea.KeyRight, tea.ModCtrl))
	if m.settings.tab != 1 {
		t.Fatalf("ctrl+→ tab = %d, want 1", m.settings.tab)
	}
	m.Update(testModifiedKey(tea.KeyLeft, tea.ModCtrl))
	m.Update(testModifiedKey(tea.KeyLeft, tea.ModCtrl))
	if m.settings.tab != len(settingsTitles)-1 {
		t.Fatalf("ctrl+← wrap tab = %d, want %d", m.settings.tab, len(settingsTitles)-1)
	}
	m.Update(testKey(tea.KeyEscape))
	if m.settings.open {
		t.Fatal("esc did not close the settings panel")
	}
	if m.sending || m.focusMode {
		t.Fatal("esc leaked past the panel")
	}
}

func TestSettingsCommandOpensPanel(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	m := newTestModelWithHiddenLogs()
	if _, ok := m.runCommand("/settings"); !ok || !m.settings.open {
		t.Fatal("/settings did not open the panel")
	}
}

func TestSettingsEveryTabShowsTabHint(t *testing.T) {
	m := openSettingsModel(t)
	for tab := range settingsTitles {
		m.settings.tab = tab
		view := ansi.Strip(m.settingsView(""))
		if !strings.Contains(view, "ctrl+←/→ tabs") || !strings.Contains(view, settingsTitles[tab]) {
			t.Fatalf("tab %d view lacks the tab hint:\n%s", tab, view)
		}
	}
}

func TestSettingsViewFieldCallsSetter(t *testing.T) {
	m := openSettingsModel(t)
	m.Update(testKey(tea.KeyRight)) // mouse: on -> off
	if m.mouseMode != mouseOff {
		t.Fatalf("mouse mode = %v, want off", m.mouseMode)
	}
	if got := loadPrefs(m.prefsPath).MouseMode; got != "off" {
		t.Fatalf("saved mouse mode = %q, want off", got)
	}
}

func TestSettingsSessionModeReachesPolicy(t *testing.T) {
	m := openSettingsModel(t)
	m.Update(testModifiedKey(tea.KeyRight, tea.ModCtrl))
	*m.settings.fields[1][1].value = "auto" // the Mode select
	m.Update(struct{}{})
	if m.policy.Mode() != approval.Auto {
		t.Fatalf("mode = %s, want auto", m.policy.Mode())
	}
}

func TestSettingsProfileSelectSwitches(t *testing.T) {
	home := t.TempDir()
	dir := config.ProfilesDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fast.md"), []byte("---\nmodel: m-fast\n---\nbe fast\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTestModelWithHiddenLogs()
	m.width, m.height = 120, 40
	t.Setenv("INVOKR_HOME", home)
	m.openSettings()
	m.settings.tab = 2
	m.Update(testKey(tea.KeyRight)) // (current) -> fast
	if got := currentModel(m.session); got != "m-fast" {
		t.Fatalf("model after profile switch = %q, want m-fast", got)
	}
}

func TestUsageCommandRemoved(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.runCommand("/usage")
	if last := m.chatLog.messages[len(m.chatLog.messages)-1].text; !strings.Contains(last, "unknown command") {
		t.Fatalf("/usage reply = %q, want unknown command", last)
	}
	if findCommand("context") == nil {
		t.Fatal("/context missing")
	}
}

// The Setup tab edits the file, so an INVOKR_* override in the environment must
// not appear as its value: Save would write the temporary override to disk.
func TestSettingsSetupReadsFileNotEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	t.Setenv("INVOKR_MODEL", "env-override")
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":{"base_url":"http://file:1/v1","model":"file-model"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTestModelWithHiddenLogs()
	m.width, m.height = 120, 40
	m.setupSettings()
	if got := m.settings.setup; got.model != "file-model" || got.baseURL != "http://file:1/v1" {
		t.Fatalf("setup draft = %+v, want the config file's values", got)
	}
}

func TestSettingsResetToolTemplatesWithoutSavingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("INVOKR_HOME", home)
	if err := setup.WriteToolTemplates(home, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(setup.ToolTemplatesDir(home), "planner.json")
	if err := os.WriteFile(path, []byte(`{"edited":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTestModelWithHiddenLogs()
	m.settings.setup.resetTemplates = true
	m.saveSettingsTab(setupTab)
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "edited") {
		t.Fatalf("template was not reset: %s %v", data, err)
	}
	if _, err := os.Stat(config.DefaultConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("reset wrote config.json: %v", err)
	}
}
