package agentui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// openModelPicker drives the real /model path: bare command -> async list ->
// overlay, so the test covers the wiring and not just the picker in isolation.
func openModelPicker(t *testing.T, session *fakeSession) *model {
	t.Helper()
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)
	m.width, m.height = 120, 24
	m.input.SetValue("/model")

	_, cmd := m.Update(testKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected an async cmd for bare /model")
	}
	if _, c := m.Update(cmd()); c != nil {
		t.Fatalf("handling the model list should not schedule more work, got %v", c)
	}
	if !m.modelPicker.open {
		t.Fatal("expected the model picker to open")
	}
	return m
}

func TestModelPickerOpensAndListsModels(t *testing.T) {
	m := openModelPicker(t, &fakeSession{model: "m1", models: []string{"m1", "m2", "m3"}})
	view := ansi.Strip(m.mainView())
	for _, want := range []string{"m1", "m2", "m3", "active", "enter switch · esc cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q: %q", want, view)
		}
	}
}

func TestModelPickerFiltersAndCountsAsYouType(t *testing.T) {
	m := openModelPicker(t, &fakeSession{model: "gpt-4o", models: []string{"gpt-4o", "gemma-9b", "gemma-27b"}})
	for _, r := range "gemma" {
		m.Update(testTextKey(string(r)))
	}
	// Scoped to the overlay: the banner carries the current model's name too.
	view := ansi.Strip(m.inputArea())
	if !strings.Contains(view, "2 of 3") {
		t.Fatalf("picker should count filtered matches: %q", view)
	}
	if strings.Contains(view, "gpt-4o") {
		t.Fatalf("filtered-out model still rendered: %q", view)
	}
}

func TestModelPickerEnterSwitchesModel(t *testing.T) {
	session := &fakeSession{model: "m1", models: []string{"m1", "m2"}}
	m := openModelPicker(t, session)
	m.Update(testKey(tea.KeyDown))
	m.Update(testKey(tea.KeyEnter))

	if m.modelPicker.open {
		t.Fatal("enter should close the picker")
	}
	if session.model != "m2" {
		t.Fatalf("session model = %q, want m2", session.model)
	}
	if !strings.Contains(ansi.Strip(m.chatContent()), "model set to m2") {
		t.Fatalf("expected switch feedback in the transcript: %q", m.chatContent())
	}
}

func TestModelPickerEscCancelsWithoutSwitching(t *testing.T) {
	session := &fakeSession{model: "m1", models: []string{"m1", "m2"}}
	m := openModelPicker(t, session)
	m.Update(testKey(tea.KeyDown))
	m.Update(testKey(tea.KeyEscape))

	if m.modelPicker.open {
		t.Fatal("esc should close the picker")
	}
	if session.model != "m1" {
		t.Fatalf("esc must not switch models, got %q", session.model)
	}
}

func TestModelPickerReplacesInputAndKeepsHeight(t *testing.T) {
	m := openModelPicker(t, &fakeSession{model: "m1", models: []string{"m1", "m2"}})
	if strings.Contains(ansi.Strip(m.inputArea()), "message") {
		t.Fatal("the picker should take the input area's place while open")
	}
	if got := strings.Count(m.mainView(), "\n") + 1; got > m.height {
		t.Fatalf("view is %d lines, exceeds terminal height %d", got, m.height)
	}
}

func TestModelPickerEmptyListFallsBackToMessage(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{model: "m1"}, nil, nil, nil, false, nil, nil)
	m.Update(modelListMsg{current: "m1"})
	if m.modelPicker.open {
		t.Fatal("an empty model list should not open a picker with nothing to pick")
	}
	if !strings.Contains(ansi.Strip(m.chatContent()), "current: m1") {
		t.Fatalf("expected the current model reported instead: %q", m.chatContent())
	}
}
