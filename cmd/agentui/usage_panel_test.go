package agentui

import (
"strings"
"testing"

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
