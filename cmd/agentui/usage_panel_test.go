package agentui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-ai/invokr/platform/turn"
)

// TestUsageEighthsIsValidUTF8 covers the byte-slice bug: the eighth blocks are
// multi-byte, so indexing the marks table by byte cut every glyph in half and
// painted invalid UTF-8 on the right border.
func TestUsageEighthsIsValidUTF8(t *testing.T) {
	for part := 1; part <= 7; part++ {
		for _, full := range []int{0, 1} {
			tokens := int64(part + full*8)
			bar := usageEighths(tokens, 128)
			if strings.ContainsRune(bar, '\uFFFD') {
				t.Fatalf("bar for part %d, full %d is not valid UTF-8: %q", part, full, bar)
			}
			marks := []rune(" ▏▎▍▌▋▊▉█")
			want := strings.Repeat("█", full) + string(marks[part])
			if bar != want {
				t.Fatalf("bar for part %d, full %d = %q, want %q", part, full, bar, want)
			}
		}
	}
}

// TestUsageReasoningPartsClampsEstimated covers the char estimate of the
// thinking text outrunning the provider's completion counts: the printed share
// must stay at or under 100%, and an answer of nothing is dropped rather than
// shown as "answer 0".
func TestUsageReasoningPartsClampsEstimated(t *testing.T) {
	usage := turn.TokenUsage{LatestCompletionTokens: 14, SessionCompletionTokens: 14}
	breakdown := turn.ContextBreakdown{ReasoningEstimated: true, ReasoningTokens: 36, SessionReasoningTokens: 36}
	last, session := usageReasoningParts(usage, breakdown)
	if !strings.Contains(last, "reasoning ~14") || strings.Contains(last, "answer") {
		t.Fatalf("last-turn line over an over-estimate = %q, want a clamp and no answer part", last)
	}
	if !strings.Contains(session, "reasoning ~14 of 14 (100%)") || strings.Contains(session, "257%") {
		t.Fatalf("session line over an over-estimate = %q", session)
	}
}

// TestUsagePanelHintOmitsEscClose: the overlay already paints its own esc close
// footer, so the panel must not say it twice.
func TestUsagePanelHintOmitsEscClose(t *testing.T) {
	usage := turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 100, LatestCompletionTokens: 50}
	breakdown := usageBreakdownOf(usage, turn.ContextBreakdown{})
	got := usagePanelLines(usage, breakdown, usageGroups(breakdown), 0, false, 85)
	if !strings.Contains(got, "↑↓ select · enter expand other") || strings.Contains(got, "esc close") {
		t.Fatalf("panel hint = %q, want the hint without a second esc close", got)
	}
}

// TestUsagePanelMatchesSpecColumns renders the spec's example breakdown and
// pins the text columns to the spec's layout. The spec's sample bars are not
// drawn to its own 16-cell scale, so bars are checked for a shared start
// column instead of compared glyph for glyph.
func TestUsagePanelMatchesSpecColumns(t *testing.T) {
	setup := usageGroup{Name: "setup", Tokens: 6670, Rows: []usageRow{
		{Label: "tool schemas", Tokens: 4948}, {Label: "tool specs", Tokens: 906}, {Label: "instructions", Tokens: 816},
	}}
	history := usageGroup{Name: "history", Tokens: 106099, Rows: []usageRow{
		{Label: "tool results", Tokens: 83790}, {Label: "tool calls", Tokens: 16159}, {Label: "assistant", Tokens: 9002},
		{Label: "other", Tokens: 2443, Members: []usageMember{{Label: "you"}, {Label: "last reply"}}},
	}}
	groups := []usageGroup{setup, history}
	const window = 204800
	lines := usageGroupLines(groups, window, usageLargestRow(groups), usagePanelStart(groups), false)
	lines = append(lines, usageFreeLine(92031, window))
	for index, line := range lines {
		lines[index] = ansi.Strip(line)
	}
	want := []string{
		"■ setup                     6,670   3.3%",
		"    tool schemas   4,948  ",
		"    tool specs       906  ",
		"    instructions     816  ",
		"■ history                 106,099  51.8%",
		"  ▸ tool results  83,790  ",
		"    tool calls    16,159  ",
		"    assistant      9,002  ",
		"    other          2,443  ",
		"□ free                     92,031  44.9%",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	const barColumn = 26
	for index, line := range lines {
		if !strings.HasPrefix(line, want[index]) {
			t.Errorf("line %d = %q, want prefix %q", index, line, want[index])
		}
		if strings.HasPrefix(want[index], "    ") || strings.HasPrefix(want[index], "  ▸") {
			bar := []rune(line)[barColumn:]
			if len(bar) == 0 || bar[0] == ' ' {
				t.Errorf("line %d: bar does not start at column %d: %q", index, barColumn, line)
			}
		}
	}
	other := lines[8]
	if got := strings.Index(other, "you, last reply"); lipgloss.Width(other[:got]) != barColumn+usageRowBarScale+2 {
		t.Errorf("other's members start at column %d, want %d: %q", lipgloss.Width(other[:got]), barColumn+usageRowBarScale+2, other)
	}
}
