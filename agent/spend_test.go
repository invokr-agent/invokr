package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestSpendCapInertWithoutProviderUsage(t *testing.T) {
	sc := newSpendCap(config.AgentConfig{MaxSessionTokens: 1}, config.PricingConfig{PromptPer1M: 100}, zap.NewNop())
	usage := turn.NewSessionTokenUsage(0)
	usage = usage.AccumulateTurnUsage(turn.TokenUsage{TurnTotalTokens: 9999})
	if reason := sc.exceeded(usage); reason != "" {
		t.Fatalf("cap tripped without provider usage: %q", reason)
	}
	// The request cap is the one bound that does not go inert here.
	backstop := newSpendCap(config.AgentConfig{MaxRequestTokens: 100}, config.PricingConfig{}, zap.NewNop())
	backstop.estimated = 100
	if reason := backstop.exceeded(usage); !strings.Contains(reason, "estimated tokens") {
		t.Fatalf("request cap reason = %q, want the local-estimate backstop to hold", reason)
	}
}

func TestSpendCapTripsOnTokensAndCost(t *testing.T) {
	usage := turn.NewSessionTokenUsage(0).AccumulateTurnUsage(turn.TokenUsage{
		Available: true, TurnPromptTokens: 600, TurnCompletionTokens: 400, TurnTotalTokens: 1000,
	})
	tokenCap := newSpendCap(config.AgentConfig{MaxSessionTokens: 1000}, config.PricingConfig{}, zap.NewNop())
	if reason := tokenCap.exceeded(usage); !strings.Contains(reason, "token budget") {
		t.Fatalf("token cap reason = %q", reason)
	}
	// 600 prompt @ 10/1M + 400 completion @ 20/1M = 0.014
	costCap := newSpendCap(config.AgentConfig{MaxSessionCost: 0.01}, config.PricingConfig{PromptPer1M: 10, CompletionPer1M: 20}, zap.NewNop())
	if reason := costCap.exceeded(usage); !strings.Contains(reason, "cost budget") {
		t.Fatalf("cost cap reason = %q", reason)
	}
	// A cost cap without pricing stays inert rather than guessing.
	unpriced := newSpendCap(config.AgentConfig{MaxSessionCost: 0.01}, config.PricingConfig{}, zap.NewNop())
	if reason := unpriced.exceeded(usage); reason != "" {
		t.Fatalf("unpriced cost cap tripped: %q", reason)
	}
}

func TestSpendCapMetersSummarizerSpend(t *testing.T) {
	usage := turn.NewSessionTokenUsage(0).AccumulateTurnUsage(turn.TokenUsage{
		Available: true, TurnPromptTokens: 400, TurnCompletionTokens: 100,
	})
	sc := newSpendCap(config.AgentConfig{MaxSessionTokens: 1000, MaxRequestTokens: 1000}, config.PricingConfig{}, zap.NewNop())
	sc.beginRequest()
	if reason := sc.exceeded(usage); reason != "" {
		t.Fatalf("cap tripped before summarizer spend: %q", reason)
	}
	sc.observeSummarizer(400, 100)
	if reason := sc.exceeded(usage); !strings.Contains(reason, "session token budget") {
		t.Fatalf("session cap ignored summarizer spend: %q", reason)
	}
	// Cost cap counts it too.
	costCap := newSpendCap(config.AgentConfig{MaxSessionCost: 0.005}, config.PricingConfig{PromptPer1M: 10, CompletionPer1M: 10}, zap.NewNop())
	costCap.observeSummarizer(400_000, 100_000)
	if reason := costCap.exceeded(usage); !strings.Contains(reason, "cost budget") {
		t.Fatalf("cost cap ignored summarizer spend: %q", reason)
	}
	// beginRequest re-baselines the per-request charge but not the session one.
	sc.beginRequest()
	if reason := sc.requestExceeded(usage); reason != "" {
		t.Fatalf("request cap charged summarizer spend: %q", reason)
	}
	if reason := sc.exceeded(usage); !strings.Contains(reason, "session token budget") {
		t.Fatalf("session cap forgot summarizer spend: %q", reason)
	}
}

// TestRequestCapIgnoresCompactionSpend covers #496: a long run compacts many
// times, and charging those summarizer completions to the per-request cap ended
// a marathon at turn 112 of 400 with two thirds of the cap spent on compaction.
func TestRequestCapIgnoresCompactionSpend(t *testing.T) {
	usage := turn.NewSessionTokenUsage(0).AccumulateTurnUsage(turn.TokenUsage{
		Available: true, TurnPromptTokens: 1000, TurnCompletionTokens: 0,
	})
	sc := newSpendCap(config.AgentConfig{MaxRequestTokens: 100_000, MaxSessionTokens: 100_000}, config.PricingConfig{}, zap.NewNop())
	sc.beginRequest()
	for range 16 {
		sc.observeSummarizer(16_000, 500)
	}
	if reason := sc.requestExceeded(usage); reason != "" {
		t.Fatalf("request cap charged 264k of compaction spend: %q", reason)
	}
	// The estimate backstop must not see it either.
	sc.estimated = 99_000
	if reason := sc.requestExceeded(turn.NewSessionTokenUsage(0)); reason != "" {
		t.Fatalf("estimate backstop charged compaction spend: %q", reason)
	}
	// The session cap still bills every token of it.
	if reason := sc.exceeded(usage); !strings.Contains(reason, "session token budget") {
		t.Fatalf("session cap = %q, want it to see the compaction spend", reason)
	}
}

func TestSpendCapEndsRunViaGracefulExhaustion(t *testing.T) {
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "demo", Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })
	toolCall := `<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`
	turns := []ModelTurn{
		{Completion: completionOutput{
			content: toolCall,
			usage:   turn.TokenUsage{Available: true, TurnPromptTokens: 40, TurnCompletionTokens: 10, TurnTotalTokens: 50},
		}},
		{Completion: completionOutput{content: `{"type":"reply","message":"partial answer"}`}},
	}

	gateway := &sequenceGateway{turns: turns}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	runner := &Runner{
		modelGateway: gateway,
		turnProcessor: &TurnProcessor{
			events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop(),
		},
		events:   bus,
		settings: config.AgentConfig{MaxTurns: 10, MaxSessionTokens: 50},
		logger:   zap.NewNop(),
		usage:    turn.NewSessionTokenUsage(0),
		spend:    newSpendCap(config.AgentConfig{MaxSessionTokens: 50}, config.PricingConfig{PromptPer1M: 1000, CompletionPer1M: 1000}, zap.NewNop()),
	}

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasSuffix(resp.Message, "partial answer") {
		t.Fatalf("Message = %q, want the graceful best-effort answer", resp.Message)
	}
	if gateway.next != 2 {
		t.Fatalf("model called %d times, want 2 (one tool turn + one graceful turn)", gateway.next)
	}
	if resp.Usage.SessionCost <= 0 {
		t.Fatalf("SessionCost = %v, want a priced session", resp.Usage.SessionCost)
	}
}

// TestSummarizeConversationLogsSpendPerCompaction covers #496 item 4: bench
// reports had to infer compaction spend by subtracting turn totals from the cap.
func TestSummarizeConversationLogsSpendPerCompaction(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	gateway := &OpenAIModelGateway{
		provider: &scriptedProvider{outputs: []*inference.Response{{
			Message: inference.AssistantMessage("summary"),
			Usage:   inference.Usage{PromptTokens: 16_000, CompletionTokens: 500},
		}}},
		logger: zap.New(core), recordSummarizerSpend: func(int64, int64) {},
	}
	if _, err := gateway.SummarizeConversation(context.Background(), []inference.Message{inference.UserMessage("hi")}); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	entries := logs.FilterMessage("context.compacted").All()
	if len(entries) != 1 {
		t.Fatalf("got %d context.compacted lines, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["summarizer_prompt_tokens"] != int64(16_000) || fields["summarizer_completion_tokens"] != int64(500) {
		t.Fatalf("logged spend = %v, want 16000/500", fields)
	}
}
