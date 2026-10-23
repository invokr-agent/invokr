package agent

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/agentprompt"
	"github.com/invokr-ai/invokr/platform/turn"

	inference "github.com/rivt-ai/go-inference-router"
)

func breakdownFixture() ([]agentprompt.PromptSection, []inference.Message) {
	sections := []agentprompt.PromptSection{
		{Name: "base prompt", Chars: 400},
		{Name: "skills", Chars: 200},
	}
	messages := []inference.Message{
		inference.SystemMessage("system prompt already attributed via sections"),
		inference.UserMessage("what is in pkg?"),
		inference.AssistantMessage("checking", inference.ToolCall{ID: "c1", Name: "shell", Arguments: `{"command":"ls"}`}),
		inference.ToolMessage("c1", "main.go\nutil.go"),
	}
	return sections, messages
}

func categoryTokens(breakdown turn.ContextBreakdown) map[string]int64 {
	got := map[string]int64{}
	for _, category := range breakdown.Categories {
		got[category.Name] = category.Tokens
	}
	return got
}

func TestBuildContextBreakdownReconcilesAgainstProviderTotal(t *testing.T) {
	sections, messages := breakdownFixture()
	usage := turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 400, LatestCompletionTokens: 50}
	breakdown := buildContextBreakdown(sections, 120, messages, 1, usage)
	if !breakdown.Available || breakdown.Estimated {
		t.Fatalf("expected available, reconciled breakdown, got %+v", breakdown)
	}
	got := categoryTokens(breakdown)
	for _, want := range []string{"base prompt", "skills", "tool schemas", "user messages", "assistant messages", "tool calls", "tool results", "unattributed", "completion"} {
		if got[want] <= 0 {
			t.Fatalf("category %q missing or zero in %v", want, breakdown.Categories)
		}
	}
	var promptSum int64
	for name, tokens := range got {
		if name != "completion" {
			promptSum += tokens
		}
	}
	if promptSum != usage.LatestPromptTokens {
		t.Fatalf("prompt-side categories sum to %d, provider reported %d", promptSum, usage.LatestPromptTokens)
	}
	if want := usage.ContextWindowTokens - usage.LatestPromptTokens - usage.LatestCompletionTokens; breakdown.FreeTokens != want {
		t.Fatalf("FreeTokens = %d, want %d", breakdown.FreeTokens, want)
	}
}

func TestBuildContextBreakdownEstimatedWithoutProviderTotal(t *testing.T) {
	sections, messages := breakdownFixture()
	breakdown := buildContextBreakdown(sections, 120, messages, 1, turn.TokenUsage{ContextWindowTokens: 1000})
	if !breakdown.Estimated {
		t.Fatalf("expected Estimated without provider usage, got %+v", breakdown)
	}
	got := categoryTokens(breakdown)
	if got["unattributed"] != 0 || got["completion"] != 0 {
		t.Fatalf("estimated breakdown must not carry unattributed/completion rows: %v", breakdown.Categories)
	}
	var sum int64
	for _, tokens := range got {
		sum += tokens
	}
	if want := int64(1000) - sum; breakdown.FreeTokens != want {
		t.Fatalf("FreeTokens = %d, want %d", breakdown.FreeTokens, want)
	}
}

func TestBuildContextBreakdownDropsZeroCategories(t *testing.T) {
	breakdown := buildContextBreakdown([]agentprompt.PromptSection{{Name: "base prompt", Chars: 40}}, 0, nil, 1, turn.TokenUsage{ContextWindowTokens: 1000})
	for _, category := range breakdown.Categories {
		if category.Tokens == 0 {
			t.Fatalf("zero-token category %q survived: %v", category.Name, breakdown.Categories)
		}
	}
	if len(buildContextBreakdown(nil, 0, nil, 1, turn.TokenUsage{}).Categories) != 0 {
		t.Fatalf("empty inputs must yield an unavailable breakdown")
	}
}

func TestCompletionFromResponseKeepsCachedAndReasoning(t *testing.T) {
	resp := &inference.Response{Usage: inference.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, CachedPromptTokens: 80, ReasoningTokens: 5}}
	usage := completionFromResponse(resp).usage
	if usage.LatestCachedPromptTokens != 80 || usage.LatestReasoningTokens != 5 {
		t.Fatalf("cached/reasoning not retained: %+v", usage)
	}
	carried := turn.TokenUsage{}.AccumulateTurnUsage(usage)
	if carried.LatestCachedPromptTokens != 80 || carried.LatestReasoningTokens != 5 {
		t.Fatalf("AccumulateTurnUsage dropped cached/reasoning: %+v", carried)
	}
}

func usageAt(prompt, completion int64) turn.TokenUsage {
	return turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: prompt,
		LatestCompletionTokens: completion}
}

// promptSideTotal sums the categories that sit in the window, which must equal
// the provider's prompt total once one is reported.
func promptSideTotal(breakdown turn.ContextBreakdown) int64 {
	var total int64
	for _, category := range breakdown.Categories {
		if category.Name != "completion" {
			total += category.Tokens
		}
	}
	return total
}

// TestBuildContextBreakdownKeepsSetupRowsSteadyAcrossPromptTotals covers the
// manual audit that watched setup grow every turn (4,246 -> 4,284 -> 4,452,
// tool schemas 3,185 -> 3,214 -> 3,340): scaling the whole legend onto the
// provider total moved the estimate error onto the setup rows each turn, which
// read as the fixed cost changing when nothing about the request changed. The
// setup rows hold their estimates and only the transcript rows absorb the
// difference, so the same setup estimate over two prompt totals gives back
// identical setup rows.
func TestBuildContextBreakdownKeepsSetupRowsSteadyAcrossPromptTotals(t *testing.T) {
	sections, messages := breakdownFixture()
	first := buildContextBreakdown(sections, 120, messages, 1, usageAt(400, 50))
	second := buildContextBreakdown(sections, 120, messages, 1, usageAt(600, 60))
	got, over := categoryTokens(first), categoryTokens(second)
	for _, name := range []string{"base prompt", "skills", "tool schemas"} {
		if got[name] != over[name] {
			t.Fatalf("setup row %q moved %d -> %d across prompt totals 400 and 600", name,
				got[name], over[name])
		}
	}
	for _, want := range []turn.ContextBreakdown{first, second} {
		if total := promptSideTotal(want); total != want.Categories[0].Tokens*0+total {
			_ = total
		}
	}
	if want := promptSideTotal(first); want != 400 {
		t.Fatalf("prompt-side rows sum to %d over a 400 token total", want)
	}
	if want := promptSideTotal(second); want != 600 {
		t.Fatalf("prompt-side rows sum to %d over a 600 token total", want)
	}
	// the transcript rows hold the room the provider total leaves, so the
	// unattributed row is what moves between the two totals
	if got["unattributed"] == over["unattributed"] {
		t.Fatalf("unattributed stayed at %d over both totals, want the transcript to absorb the difference",
			got["unattributed"])
	}
	if got["unattributed"] != 204 || over["unattributed"] != 404 {
		t.Fatalf("unattributed %d -> %d, want 204 -> 404", got["unattributed"], over["unattributed"])
	}
}

// TestBuildContextBreakdownScalesHistoryOntoTheRoomLeft covers the transcript
// overshooting the room the setup rows leave: only the transcript rows shrink,
// so the setup rows still hold their estimates.
func TestBuildContextBreakdownScalesHistoryOntoTheRoomLeft(t *testing.T) {
	sections, messages := breakdownFixture()
	// the setup rows estimate 180 tokens, so a 190 token total leaves 10 tokens
	// of room for the 16 tokens of transcript estimates
	breakdown := buildContextBreakdown(sections, 120, messages, 1, usageAt(190, 50))
	got := categoryTokens(breakdown)
	for _, name := range []string{"base prompt", "skills", "tool schemas"} {
		if want := map[string]int64{"base prompt": 100, "skills": 50, "tool schemas": 30}; got[name] != want[name] {
			t.Fatalf("setup row %q = %d, want its %d token estimate", name, got[name], want[name])
		}
	}
	if want := promptSideTotal(breakdown); want != 190 {
		t.Fatalf("prompt-side rows sum to %d, want 190", want)
	}
	// the transcript rows shrink onto the room: each takes its share's floor,
	// then the two tokens the flooring left over go to the largest fractional
	// remainders, the tool calls and the user message
	if got["tool calls"] != 4 || got["tool results"] != 2 {
		t.Fatalf("transcript rows took %d calls and %d results, want 4 and 2",
			got["tool calls"], got["tool results"])
	}
	if got["user messages"] != 3 || got["assistant messages"] != 1 {
		t.Fatalf("transcript rows took %d user and %d assistant, want 3 and 1",
			got["user messages"], got["assistant messages"])
	}
	if got["unattributed"] != 0 {
		t.Fatalf("a scaled transcript still asked for an unattributed row of %d", got["unattributed"])
	}
}

// TestBuildContextBreakdownScalesSetupWhenItPassesTheTotal covers the one case
// that cannot keep the setup rows at their estimates: the provider total is
// under the setup estimate on its own, so the whole legend has to shrink or the
// bar reads over 100%.
func TestBuildContextBreakdownScalesSetupWhenItPassesTheTotal(t *testing.T) {
	sections, messages := breakdownFixture()
	// the setup rows estimate 180 tokens over a 90 token total
	breakdown := buildContextBreakdown(sections, 120, messages, 1, usageAt(90, 50))
	got := categoryTokens(breakdown)
	if want := promptSideTotal(breakdown); want != 90 {
		t.Fatalf("prompt-side rows sum to %d, want 90", want)
	}
	if got["base prompt"] >= 100 || got["skills"] >= 50 {
		t.Fatalf("setup rows held their estimates over a total they pass: %v", breakdown.Categories)
	}
	if got["base prompt"] != 46 || got["skills"] != 23 || got["tool schemas"] != 14 {
		t.Fatalf("setup rows scaled to %d/%d/%d, want 46/23/14",
			got["base prompt"], got["skills"], got["tool schemas"])
	}
}

// TestBuildContextBreakdownReasoningStaysUnderCompletion covers the audit line
// that read "reasoning ~377 of 377 (100%)" with no answer: strata reports the
// thinking text inside completion_tokens (completion_tokens 29 for a "Hi"
// answer plus ~24 tokens of reasoning_content), so a chars/4 estimate of the
// thinking could meet or beat that total and the clamp hid the answer. The
// estimate now takes the thinking text's share of the provider's completion
// total, which keeps the share under 100% and the answer a line of its own.
func TestBuildContextBreakdownReasoningStaysUnderCompletion(t *testing.T) {
	sections, messages := breakdownFixture()
	// one thinking turn: 120 chars of thinking over 80 chars of answer, and the
	// provider measured 40 completion tokens for both
	thinkingTurn := inference.AssistantMessage(strings.Repeat("a", 80), inference.ToolCall{ID: "c1", Name: "shell", Arguments: "{}"})
	thinkingTurn.Reasoning = strings.Repeat("t", 120)
	usage := turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 400,
		LatestCompletionTokens: 40, SessionCompletionTokens: 40}
	breakdown := buildContextBreakdown(sections, 120, append(messages, thinkingTurn), 1, usage)
	// strata reports no reasoning count of its own, so the pair stays marked as
	// an estimate, but the estimate is now sized off the completion total
	if !breakdown.ReasoningEstimated {
		t.Fatalf("a provider that reports no reasoning count must stay marked as estimated: %+v", breakdown)
	}
	if breakdown.ReasoningTokens >= usage.LatestCompletionTokens || breakdown.ReasoningTokens <= 0 {
		t.Fatalf("reasoning estimate %d over a %d token completion, want a share under it",
			breakdown.ReasoningTokens, usage.LatestCompletionTokens)
	}
	// the thinking's own share of the 200 chars of the newest turn is 120/200 of
	// the 40 completion tokens
	if breakdown.ReasoningTokens != 24 {
		t.Fatalf("reasoning estimate %d, want 24 (120 thinking chars of 200 over 40 completion tokens)",
			breakdown.ReasoningTokens)
	}
	// the session estimate holds the same ratio over its own completion total
	if breakdown.SessionReasoningTokens != breakdown.ReasoningTokens {
		t.Fatalf("session reasoning %d over latest %d, want the same single thinking turn",
			breakdown.SessionReasoningTokens, breakdown.ReasoningTokens)
	}
}
