package agent

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// reported is one model call's provider-reported usage.
func reported(prompt, completion int64) turn.TokenUsage {
	return turn.TokenUsage{Available: true, LatestPromptTokens: prompt, LatestCompletionTokens: completion,
		TurnPromptTokens: prompt, TurnCompletionTokens: completion, TurnTotalTokens: prompt + completion}
}

// usageRunner drives a two-model-call request (one tool turn, then the answer)
// with pricing and a provider window set, and returns the usage events the
// request published.
func usageRunner(t *testing.T) (*Runner, *[]events.Event) {
	t.Helper()
	cfg := config.AgentConfig{MaxTurns: 2}
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "demo", Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })
	gateway := &sequenceGateway{turns: []ModelTurn{
		{Completion: completionOutput{toolCalls: []inference.ToolCall{{ID: "c1", Name: "demo", Arguments: `{}`}},
			usage: reported(1_000, 100)}},
		{Completion: completionOutput{content: `{"type":"reply","message":"done"}`, usage: reported(1_200, 200)}},
	}}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	published := new([]events.Event)
	unregister := bus.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventUsage {
			*published = append(*published, ev)
		}
		return nil
	}))
	t.Cleanup(unregister)
	return &Runner{
		modelGateway: gateway,
		turnProcessor: &TurnProcessor{
			events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop(),
		},
		events:          bus,
		settings:        cfg,
		logger:          zap.NewNop(),
		usage:           turn.NewSessionTokenUsage(4_000),
		spend:           newSpendCap(cfg, config.PricingConfig{PromptPer1M: 500_000, CompletionPer1M: 1_000_000}, zap.NewNop()),
		providerContext: NewProviderContext(4_000, "m"),
	}, published
}

// TestUsageEventPerModelCall covers issue #82: every model call publishes the
// turn's running usage, so a surface can meter the context window mid-turn. The
// payload carries the provider's window and the cost the final response reports.
func TestUsageEventPerModelCall(t *testing.T) {
	runner, published := usageRunner(t)
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(*published) != 2 {
		t.Fatalf("usage events = %d, want one per model call", len(*published))
	}
	first, second := (*published)[0].Usage, (*published)[1].Usage
	if first.TurnPromptTokens != 1_000 || first.LatestCompletionTokens != 100 {
		t.Fatalf("first usage = %+v, want the first call's spend", first)
	}
	if second.TurnPromptTokens != 2_200 || second.TurnCompletionTokens != 300 {
		t.Fatalf("second usage = %+v, want the running total", second)
	}
	for _, u := range []turn.TokenUsage{*first, *second} {
		if u.ContextWindowTokens != 4_000 {
			t.Fatalf("usage window = %d, want the provider's 4000", u.ContextWindowTokens)
		}
		if u.SessionCost <= 0 {
			t.Fatalf("usage is not priced: %+v", u)
		}
	}
	if second.SessionCost != resp.Usage.SessionCost {
		t.Fatalf("last event cost = %v, want the final response's %v", second.SessionCost, resp.Usage.SessionCost)
	}
	if resp.Usage.TurnPromptTokens != 2_200 || resp.Usage.TurnCompletionTokens != 300 {
		t.Fatalf("final per-turn usage changed: %+v", resp.Usage)
	}
}

// TestUsageEventAbsentWithoutReportedUsage covers a provider that reports no
// usage: nothing is published, so no surface is handed a made-up meter.
func TestUsageEventAbsentWithoutReportedUsage(t *testing.T) {
	runner, published := usageRunner(t)
	gateway := runner.modelGateway.(*sequenceGateway)
	for i := range gateway.turns {
		gateway.turns[i].Completion.usage = turn.TokenUsage{}
	}
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(*published) != 0 {
		t.Fatalf("usage events = %d, want none without reported usage", len(*published))
	}
}
