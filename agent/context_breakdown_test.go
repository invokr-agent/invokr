package agent

import (
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
