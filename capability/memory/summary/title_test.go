package summary

import (
	"strings"
	"testing"
)

// Small models habitually wrap titles in quotes, prefix them, or add a period.
func TestCleanTitleStripsModelDecorations(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`Wiring the session picker`, "Wiring the session picker"},
		{`"Wiring the session picker"`, "Wiring the session picker"},
		{"Title: Wiring the session picker", "Wiring the session picker"},
		{"Wiring the session picker.", "Wiring the session picker"},
		{"**Wiring the session picker**", "Wiring the session picker"},
		{"  Wiring the picker\nAnd some rambling after", "Wiring the picker"},
		{"", ""},
	} {
		if got := CleanTitle(tc.raw); got != tc.want {
			t.Errorf("CleanTitle(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestRenderTitlePromptRejectsEmptyTranscript(t *testing.T) {
	if _, err := RenderTitlePrompt(nil); err == nil {
		t.Fatal("empty transcript should not be titled")
	}
}

func TestRenderTitlePromptTruncatesLongTranscripts(t *testing.T) {
	prompt, err := RenderTitlePrompt([]string{"user: " + strings.Repeat("x", titleTranscriptLimit*2)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prompt, TitlePromptHeader) {
		t.Fatal("prompt is missing its instruction header")
	}
	body := strings.TrimPrefix(prompt, TitlePromptHeader)
	if len([]rune(body)) > titleTranscriptLimit {
		t.Fatalf("transcript body = %d runes, want <= %d", len([]rune(body)), titleTranscriptLimit)
	}
}
