package agentui

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/platform/turn"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderContextSegmentsBreakdown(t *testing.T) {
	usage := turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 400, LatestCompletionTokens: 50}
	breakdown := turn.ContextBreakdown{
		Available:    true,
		WindowTokens: 1000,
		FreeTokens:   550,
		Categories: []turn.ContextCategory{
			{Name: "base prompt", Tokens: 100},
			{Name: "tool results", Tokens: 250},
			{Name: "unattributed", Tokens: 50},
			{Name: "completion", Tokens: 50},
		},
	}
	out := ansi.Strip(renderContextSegments(usage, breakdown))
	for _, want := range []string{"■ base prompt", "■ tool results", "■ unattributed", "□ free", "550", "25.0%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("breakdown render missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "estimated") {
		t.Fatalf("reconciled breakdown must not be flagged estimated:\n%s", out)
	}

	breakdown.Estimated = true
	if got := ansi.Strip(renderContextSegments(usage, breakdown)); !strings.Contains(got, "estimated") {
		t.Fatalf("estimated breakdown missing flag:\n%s", got)
	}
}

func TestRenderContextSegmentsFallsBackWithoutBreakdown(t *testing.T) {
	usage := turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 400, LatestCompletionTokens: 50}
	out := ansi.Strip(renderContextSegments(usage, turn.ContextBreakdown{}))
	for _, want := range []string{"■ prompt", "■ completion", "□ free"} {
		if !strings.Contains(out, want) {
			t.Fatalf("fallback render missing %q:\n%s", want, out)
		}
	}
}

func TestLastTurnLine(t *testing.T) {
	usage := turn.TokenUsage{LatestPromptTokens: 1000, LatestCachedPromptTokens: 800, LatestReasoningTokens: 120}
	got := ansi.Strip(lastTurnLine(usage))
	if !strings.Contains(got, "reasoning 120") || !strings.Contains(got, "cached 800 (80%)") {
		t.Fatalf("last-turn line wrong: %q", got)
	}
	if lastTurnLine(turn.TokenUsage{}) != "" {
		t.Fatalf("zero usage must render no last-turn line")
	}
}

func TestContextTriggerUsesEffectiveWindow(t *testing.T) {
	usage := turn.TokenUsage{ContextWindowTokens: 61440}
	percent := compactionThreshold(nil, usage.ContextWindowTokens)
	if percent != 89 || !strings.Contains(renderContextTotals(usage, percent), "compaction triggers at 89%") {
		t.Fatalf("effective compaction trigger = %d: %s", percent, renderContextTotals(usage, percent))
	}
}

// TestSessionTotalsLineSplitsCachedPrompt covers the /context
// requirement from issue #359: cached prompt tokens are shown separately from
// the uncached ones, and a provider that reports none renders as before.
func TestSessionTotalsLineSplitsCachedPrompt(t *testing.T) {
	cached := turn.TokenUsage{SessionPromptTokens: 12000, SessionCachedPromptTokens: 9000, SessionCompletionTokens: 400}
	got := sessionTotalsLine(cached)
	for _, want := range []string{"12,000 prompt", "9,000 cached", "3,000 uncached", "400 completion"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session totals %q missing %q", got, want)
		}
	}
	plain := sessionTotalsLine(turn.TokenUsage{SessionPromptTokens: 12000, SessionCompletionTokens: 400})
	if strings.Contains(plain, "cached") {
		t.Fatalf("uncached session totals must not mention the cache: %q", plain)
	}
}
