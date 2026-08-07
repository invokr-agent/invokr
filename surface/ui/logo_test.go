package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestLogoWidth(t *testing.T) {
	for _, dark := range []bool{true, false} {
		theme := NewTheme(dark)
		// The wordmark is a multi-line block: measure its widest line, not the
		// concatenation of all of them.
		got := lipgloss.Width(theme.Logo())
		if got < LogoWidth-2 || got > LogoWidth {
			t.Fatalf("Logo() visible width = %d, want within 2 of %d (dark=%v)", got, LogoWidth, dark)
		}
	}
}

func TestBlendTextEmpty(t *testing.T) {
	if got := BlendText("", lipgloss.Color("#000000"), lipgloss.Color("#ffffff")); got != "" {
		t.Fatalf("BlendText(\"\") = %q, want empty", got)
	}
}

func TestBlendTextRampsAcrossClusters(t *testing.T) {
	got := BlendText("abcdef", lipgloss.Color("#ff0000"), lipgloss.Color("#0000ff"))
	stripped := ansi.Strip(got)
	if stripped != "abcdef" {
		t.Fatalf("BlendText content = %q, want %q", stripped, "abcdef")
	}
	// The rendered form must carry per-character SGR codes, not a single
	// style applied to the whole string, or there's no gradient at all.
	if strings.Count(got, "\x1b[") < 2 {
		t.Fatalf("BlendText output has no per-character styling: %q", got)
	}
}
