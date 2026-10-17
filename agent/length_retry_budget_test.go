package agent

import (
	"context"
	"errors"
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
)

// retryRunner builds a Runner whose only tool always fails, driven by the
// given scripted turns.
func retryRunner(t *testing.T, turns []ModelTurn) (*Runner, *sequenceGateway) {
	t.Helper()
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "demo", Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { return "", errors.New("boom") })
	gateway := &sequenceGateway{turns: turns}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	cfg := config.AgentConfig{MaxTurns: 20}
	return &Runner{
		modelGateway: gateway,
		turnProcessor: &TurnProcessor{
			events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop(),
		},
		events:   bus,
		settings: cfg,
		logger:   zap.NewNop(),
		usage:    turn.NewSessionTokenUsage(0),
		spend:    newSpendCap(cfg, config.PricingConfig{}, zap.NewNop()),
	}, gateway
}

// TestLengthRetryToolErrorContinuesTheLoop covers the #443 follow-up fix: a
// tool that ran and returned an error result after the length retry is an
// ordinary turn outcome, fed back to the model so the loop continues.
func TestLengthRetryToolErrorContinuesTheLoop(t *testing.T) {
	failingCall := `<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`
	runner, gateway := retryRunner(t, []ModelTurn{
		{Completion: completionOutput{content: failingCall, lengthRetried: true}},
		{Completion: completionOutput{content: `{"type":"reply","message":"finished"}`}},
	})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.Message != "finished" {
		t.Fatalf("answer = %q, want the answer from the continued turn", resp.Message)
	}
	next := gateway.continueRequests[len(gateway.continueRequests)-1]
	if next.DisableTools {
		t.Fatalf("run ended through the wrap-up path instead of continuing")
	}
}

// TestLengthRetryEndsRunWhenItsToolCallIsUnusable covers the other half: the
// retried turn produced no usable tool call at all, so the run ends through
// the wrap-up path instead of spending the rest of the wall clock.
func TestLengthRetryEndsRunWhenItsToolCallIsUnusable(t *testing.T) {
	unusable := `<|tool_call>{"arguments":{}}<|tool_call|>`
	runner, gateway := retryRunner(t, []ModelTurn{
		{Completion: completionOutput{content: unusable, lengthRetried: true}},
		{Completion: completionOutput{content: `{"type":"reply","message":"wrapped up"}`}},
	})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasSuffix(resp.Message, "wrapped up") {
		t.Fatalf("answer = %q, want the wrap-up answer", resp.Message)
	}
	salvage := gateway.continueRequests[len(gateway.continueRequests)-1]
	if !salvage.DisableTools {
		t.Fatalf("run did not end through the wrap-up path")
	}
	last := salvage.Messages[len(salvage.Messages)-1]
	if !strings.Contains(last.Content, "tools are now disabled") {
		t.Fatalf("last salvage message = %q, want the wrap-up instruction", last.Content)
	}
}

// TestLengthCutAfterRetryEndsRun covers the other half: the retry produced a
// working tool call, but the next completion is cut again — one retry per run
// is spent, so the run wraps up rather than continuing.
func TestLengthCutAfterRetryEndsRun(t *testing.T) {
	runner, gateway := retryRunner(t, []ModelTurn{
		{Completion: completionOutput{content: `{"type":"reply","message":"partial"}`, lengthRetried: true, lengthExhausted: true}},
		{Completion: completionOutput{content: `{"type":"reply","message":"wrapped up"}`}},
	})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasSuffix(resp.Message, "wrapped up") || gateway.next != 2 {
		t.Fatalf("answer = %q after %d calls, want the wrap-up on the second cut", resp.Message, gateway.next)
	}
}

// TestLengthRetryBudgetIsPerTurn covers the S2 regression: the retry budget is
// spent per turn, so a later turn cut on the output limit — the wrap-up call
// included — still buys its own no-thinking retry instead of being written off
// because an earlier turn used the run's only one.
func TestLengthRetryBudgetIsPerTurn(t *testing.T) {
	// The retries answer with a tool call, so no degenerate-retry nudge follows.
	act := &inference.Response{Message: inference.Message{ToolCalls: []inference.ToolCall{{ID: "c1", Name: "edit", Arguments: "{}"}}}, FinishReason: inference.FinishToolCalls}
	provider := &scriptedProvider{outputs: []*inference.Response{lengthCutResponse(""), act, lengthCutResponse(""), act}}
	gateway := testModelGateway(provider)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())
	ctx := context.Background()
	msgs := []inference.Message{inference.UserMessage("run")}

	if _, _, err := gateway.complete(ctx, msgs, "s1", false, false, 0); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	turn, err := gateway.Continue(ctx, ContinueModelRequest{SessionID: "s1", Messages: msgs})
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if provider.calls != 4 {
		t.Fatalf("provider called %d times, want each turn to get its own retry", provider.calls)
	}
	if turn.Completion.lengthExhausted {
		t.Fatalf("output = %#v, want the second turn's cut retried, not written off", turn.Completion)
	}
}

// TestLengthCutRetryRecoversTheAnswer covers the S2 bench failure: a completion
// that burned its output budget on reasoning is retried with thinking off and a
// nudge to answer directly, and that retry's answer is what the turn returns —
// the cut partial never reaches history.
func TestLengthCutRetryRecoversTheAnswer(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		lengthCutResponse("Let me try to get the exact clo"),
		{Message: inference.Message{Content: `{"type":"reply","message":"` + substantiveAnswer + `"}`}},
	}}
	gateway := testModelGateway(provider)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())
	msgs := []inference.Message{inference.UserMessage("run")}

	output, kept, err := gateway.complete(context.Background(), msgs, "s1", false, false, 0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if output.content != `{"type":"reply","message":"`+substantiveAnswer+`"}` {
		t.Fatalf("content = %q, want the retried answer", output.content)
	}
	retry := provider.requests[1]
	last := retry.Messages[len(retry.Messages)-2]
	// The scripted cut reports no usage, so no plan is kept and the nudge must
	// not claim one is above (#653 review).
	if last.Role != inference.RoleUser || last.Content != lengthRetryNudgeNoPlan {
		t.Fatalf("retry's last message = %#v, want the no-plan nudge", last)
	}
	if len(kept) != len(msgs) {
		t.Fatalf("kept %d messages, want the nudge and the cut partial left out of history", len(kept))
	}
}

// TestTruncatedWrapUpIsNotAnAnswer covers the other half of the S2 failure: the
// wrap-up completion was itself cut mid-sentence, and that severed prose was
// shipped as the final answer.
func TestTruncatedWrapUpIsNotAnAnswer(t *testing.T) {
	runner, _ := retryRunner(t, []ModelTurn{
		{Completion: completionOutput{content: `{"type":"reply","message":"partial"}`, lengthRetried: true, lengthExhausted: true}},
		{Completion: completionOutput{content: "Let me try to get the exact clo", lengthExhausted: true}},
	})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(resp.Message, "I wasn't able to finish") {
		t.Fatalf("answer = %q, want the cut wrap-up rejected", resp.Message)
	}
}
