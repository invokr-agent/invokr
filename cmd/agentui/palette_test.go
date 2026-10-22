package agentui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The palette and the completion popup must draw from the same command table.
func TestPaletteCompletionParity(t *testing.T) {
	suggestions := commandSuggestions()
	items := paletteItems()
	if len(items) != len(suggestions) {
		t.Fatalf("palette has %d entries, completion %d", len(items), len(suggestions))
	}
	for i, item := range items {
		if "/"+item.Key != suggestions[i] {
			t.Fatalf("palette[%d] = %q, completion = %q", i, item.Key, suggestions[i])
		}
		if !strings.Contains(item.Label, " — ") {
			t.Fatalf("palette entry %q carries no description: %q", item.Key, item.Label)
		}
	}
}

func TestPaletteOpensFiltersAndCloses(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.input.SetValue("draft text")
	handled, _ := m.updatePaletteKey(testModifiedKey('p', tea.ModCtrl))
	if !handled || !m.palette.open {
		t.Fatal("ctrl+p did not open the palette")
	}
	// Type-to-filter matches descriptions too ("token usage" -> /usage).
	for _, r := range "token" {
		if h, _ := m.updatePaletteKey(testTextKey(string(r))); !h {
			t.Fatal("filter key not consumed")
		}
	}
	filtered := m.palette.list.Filtered()
	if len(filtered) == 0 {
		t.Fatal("description filter matched nothing")
	}
	for _, item := range filtered {
		if !strings.Contains(strings.ToLower(item.Label), "token") {
			t.Fatalf("filter leaked %q", item.Label)
		}
	}
	// esc closes with the prompt untouched.
	if h, _ := m.updatePaletteKey(testKey(tea.KeyEscape)); !h {
		t.Fatal("esc not consumed")
	}
	if m.palette.open {
		t.Fatal("palette still open after esc")
	}
	if m.input.Value() != "draft text" {
		t.Fatalf("prompt changed: %q", m.input.Value())
	}
}

func TestPaletteEnterRunsArgumentlessCommand(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.updatePaletteKey(testModifiedKey('p', tea.ModCtrl))
	for _, r := range "clear" {
		m.updatePaletteKey(testTextKey(string(r)))
	}
	if h, _ := m.updatePaletteKey(testKey(tea.KeyEnter)); !h {
		t.Fatal("enter not consumed")
	}
	if m.palette.open {
		t.Fatal("palette still open after enter")
	}
	if m.err != ErrNewSession {
		t.Fatal("/clear did not run from the palette")
	}
}

func TestPaletteEnterPrefillsArgumentTakingCommand(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.updatePaletteKey(testModifiedKey('p', tea.ModCtrl))
	for _, r := range "progress" {
		m.updatePaletteKey(testTextKey(string(r)))
	}
	m.updatePaletteKey(testKey(tea.KeyEnter))
	if got := m.input.Value(); got != "/progress " {
		t.Fatalf("input = %q, want %q", got, "/progress ")
	}
}
