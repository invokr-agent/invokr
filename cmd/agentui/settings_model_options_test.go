package agentui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/turn"
)

type optionSession struct {
	turn.Session
	saved map[string]string
	model string
}

func (s *optionSession) ModelOptions() (string, []config.ModelOptionValue, error) {
	var values []config.ModelOptionValue
	for _, o := range config.ModelOptionRegistry {
		values = append(values, config.ModelOptionValue{Option: o, Effective: "omit", Source: "inherited startup settings", Override: s.saved[o.Key]})
	}
	return "fixture-model", values, nil
}

func (s *optionSession) SaveModelOptions(model string, values map[string]string) error {
	if err := config.SetModelOptions(config.DefaultConfigPath(), model, values); err != nil {
		return err
	}
	s.model, s.saved = model, values
	return nil
}

func TestModelOptionsMenuNavigationSaveAndCancel(t *testing.T) {
	registry := config.ModelOptionRegistry
	config.ModelOptionRegistry = append(append([]config.ModelOption(nil), registry...), config.ModelOption{
		Key: "extension_test", Label: "Future choice", Type: "choice", Choices: []string{"on", "off"},
	})
	t.Cleanup(func() { config.ModelOptionRegistry = registry })
	m := openSettingsModel(t)
	s := &optionSession{Session: m.session}
	m.session = s
	// Ctrl-left from View wraps to the registry-driven panel.
	m.Update(testModifiedKey(tea.KeyLeft, tea.ModCtrl))
	if m.settings.tab != modelOptionsTab {
		t.Fatalf("tab = %d", m.settings.tab)
	}
	m.updateSettings(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := ansi.Strip(m.settingsView(""))
	if !strings.Contains(view, "fixture-model") || !strings.Contains(view, "source: inherited") {
		t.Fatalf("missing model/provenance: %s", view)
	}
	if len(m.settings.modelOptions.values) != len(config.ModelOptionRegistry) {
		t.Fatal("registry entries missing from form")
	}
	*m.settings.modelOptions.values["temperature"] = "0"
	m.settings.modelOptions.save = true
	m.settings.forms[modelOptionsTab].State = huh.StateCompleted
	m.updateSettings(struct{}{})
	if s.model != "fixture-model" || s.saved["temperature"] != "0" {
		t.Fatalf("save not delivered: %+v", s)
	}
	*m.settings.modelOptions.values["temperature"] = "1.8"
	m.Update(testKey(tea.KeyEscape))
	if m.settings.open || s.saved["temperature"] != "0" {
		t.Fatal("escape saved draft or failed to close")
	}
}
