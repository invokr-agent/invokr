package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"

	"go.uber.org/zap"
)

// paddedToolCall is a demo tool call whose arguments are fat enough that the
// len/4 estimate charges roughly 1000 tokens for the completion alone.
func paddedToolCall() string {
	return `<|tool_call>{"name":"demo","arguments":{"pad":"` + strings.Repeat("x", 4000) + `"}}<|tool_call|>`
}

// budgetRunner builds a Runner whose gateway emits `loops` padded tool-call
// turns and then a plain reply, so an uncapped run always terminates.
func budgetRunner(t *testing.T, agentCfg config.AgentConfig, loops int, usage turn.TokenUsage) (*Runner, *sequenceGateway) {
	t.Helper()
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "demo", Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })

	turns := make([]ModelTurn, 0, loops+1)
	for range loops {
		turns = append(turns, ModelTurn{Completion: completionOutput{content: paddedToolCall(), usage: usage}})
	}
	turns = append(turns, ModelTurn{Completion: completionOutput{content: `{"type":"reply","message":"partial answer"}`}})

	gateway := &sequenceGateway{turns: turns}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	return &Runner{
		modelGateway: gateway,
		turnProcessor: &TurnProcessor{
			events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop(),
		},
		events:   bus,
		settings: agentCfg,
		logger:   zap.NewNop(),
		usage:    turn.NewSessionTokenUsage(0),
		spend:    newSpendCap(agentCfg, config.PricingConfig{}, zap.NewNop()),
	}, gateway
}

// TestRequestCapWarnsBeforeItTripsOnEstimate mirrors
// TestRequestCapWarnsBeforeItTrips for a provider that reports no usage: the
// wrap-up nudge must still land on the estimate path, not just the
// provider-reported one.
func TestRequestCapWarnsBeforeItTripsOnEstimate(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 40, MaxRequestTokens: 4000}
	runner, gateway := budgetRunner(t, cfg, 40, turn.TokenUsage{})

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	warned := false
	for _, req := range gateway.continueRequests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "Budget almost spent") {
				warned = true
			}
		}
	}
	if !warned {
		t.Fatalf("no wrap-up warning reached the model before the request cap tripped")
	}
}

func TestRequestCapTripsOnLocalEstimateWithoutProviderUsage(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 40, MaxRequestTokens: 2000}
	runner, gateway := budgetRunner(t, cfg, 40, turn.TokenUsage{})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.Message == "" {
		t.Fatalf("want a best-effort answer from gracefulExhaustion, got empty")
	}
	// ~1000 estimated tokens per padded turn against a 2000 cap: a handful of
	// turns, not the 40-turn ceiling.
	if gateway.next > 6 {
		t.Fatalf("model called %d times, want the estimate to bind well before the %d-turn ceiling", gateway.next, cfg.MaxTurns)
	}
}

func TestRequestCapPrefersProviderUsageOverEstimate(t *testing.T) {
	sc := newSpendCap(config.AgentConfig{MaxRequestTokens: 1000}, config.PricingConfig{}, zap.NewNop())
	sc.estimated = 999_999
	turnUsage := turn.TokenUsage{
		Available: true, LatestPromptTokens: 900, LatestCompletionTokens: 200,
		TurnPromptTokens: 900, TurnCompletionTokens: 200, TurnTotalTokens: 1100,
	}
	usage := turn.NewSessionTokenUsage(0).AccumulateTurnUsage(turnUsage)
	sc.observeTurn(turnUsage)
	reason := sc.exceeded(usage)
	if !strings.Contains(reason, "request token budget reached") {
		t.Fatalf("reason = %q, want the request cap", reason)
	}
	if strings.Contains(reason, "estimated") {
		t.Fatalf("reason = %q, want provider-reported totals, not the estimate", reason)
	}
}

func TestRequestCapResetsBetweenRequests(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 40, MaxRequestTokens: 2000}
	runner, gateway := budgetRunner(t, cfg, 40, turn.TokenUsage{})

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "one"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := gateway.next
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "two"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	// A session-scoped cap would give the second request a single turn before
	// giving up, permanently. Per-request means it gets the same allowance.
	if second := gateway.next - first; second < first {
		t.Fatalf("second request got %d turns, first got %d; the cap did not reset", second, first)
	}
}

func TestRepeatNudgeCapEndsRunGracefully(t *testing.T) {
	// No token caps: only the repeat guard's nudge cap can stop this loop of
	// identical tool calls before the 40-turn ceiling.
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 40}, 40, turn.TokenUsage{})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.Message == "" {
		t.Fatalf("want a best-effort answer from gracefulExhaustion, got empty")
	}
	// The guard trips every maxIdenticalToolCallRepeats identical turns; after
	// maxRepeatNudges nudges the next trip ends the run: 9 tool turns + 1 graceful.
	if want := maxIdenticalToolCallRepeats*(maxRepeatNudges+1) + 1; gateway.next != want {
		t.Fatalf("model called %d times, want %d (nudge cap should end the loop)", gateway.next, want)
	}
}

// TestRepeatNudgeCapAsksForSummaryNotBudgetAbort covers the repeat-exhaustion
// path ending with a plain "summarize what you changed" ask instead of the
// generic budget-exhausted wrap-up, which reads as an abort even when the
// work is already done (the model was just looping).
func TestRepeatNudgeCapAsksForSummaryNotBudgetAbort(t *testing.T) {
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 40}, 40, turn.TokenUsage{})
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(gateway.continueRequests) == 0 {
		t.Fatalf("want at least one Continue call")
	}
	salvage := gateway.continueRequests[len(gateway.continueRequests)-1]
	if len(salvage.Messages) == 0 {
		t.Fatalf("salvage request carried no messages")
	}
	last := salvage.Messages[len(salvage.Messages)-1].Content
	if !strings.Contains(last, "Stop calling tools. Summarize what you changed and its current state.") {
		t.Fatalf("salvage wrap-up = %q, want the repeat-exhaustion summary ask", last)
	}
	if strings.Contains(last, "budget is exhausted") {
		t.Fatalf("salvage wrap-up = %q, want no budget-exhausted framing on the repeat path", last)
	}
}

// The repeat give-up salvage runs the same way the parse give-up summary does:
// no tools and no thinking, so a model that was already looping answers the
// "stop and summarize" ask instead of spending its budget reasoning again.
func TestRepeatNudgeSalvageDisablesToolsAndThinking(t *testing.T) {
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 40}, 40, turn.TokenUsage{})
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	salvage := gateway.continueRequests[len(gateway.continueRequests)-1]
	if !salvage.DisableTools || !salvage.DisableThinking {
		t.Fatalf("salvage DisableTools=%v DisableThinking=%v, want both true", salvage.DisableTools, salvage.DisableThinking)
	}
}

// When the salvage completion comes back empty the canned apology is prefixed
// with the transcript's own ledger, so the user is told the tool calls that
// already ran rather than only that nothing could be finished.
func TestRepeatNudgeSalvageFallbackReportsCompletedWork(t *testing.T) {
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 40}, 9, turn.TokenUsage{})
	// The 10th turn is the salvage call: hand it a blank completion.
	gateway.turns[9] = ModelTurn{Completion: completionOutput{content: ""}}
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(resp.Message, "tool calls did complete first") {
		t.Fatalf("Message = %q, want the completed-work note before the apology", resp.Message)
	}
	if !strings.Contains(resp.Message, "I wasn't able to finish") {
		t.Fatalf("Message = %q, want the canned apology kept", resp.Message)
	}
}

func TestRequestCapDisabledAtZero(t *testing.T) {
	sc := newSpendCap(config.AgentConfig{MaxRequestTokens: 0}, config.PricingConfig{}, zap.NewNop())
	sc.estimated = 999_999
	if reason := sc.exceeded(turn.NewSessionTokenUsage(0)); reason != "" {
		t.Fatalf("disabled cap tripped: %q", reason)
	}
}

// TestRequestCapChargesNewContextOnly covers #445: a re-sent transcript prefix
// is charged once, not once per turn, so the cap binds on work actually done.
func TestRequestCapChargesNewContextOnly(t *testing.T) {
	sc := newSpendCap(config.AgentConfig{MaxRequestTokens: 1200}, config.PricingConfig{}, zap.NewNop())
	sc.beginRequest()
	usage := turn.NewSessionTokenUsage(0)
	// Five turns whose prompt grows 100 tokens each on top of a 400-token
	// prefix, plus 50 completion tokens each: 1050 charged as new context,
	// against 3250 if every turn were charged its whole prompt again.
	for i := range 5 {
		t := turn.TokenUsage{
			Available: true, LatestPromptTokens: int64(400 + i*100), LatestCompletionTokens: 50,
			TurnPromptTokens: int64(400 + i*100), TurnCompletionTokens: 50,
		}
		usage = usage.AccumulateTurnUsage(t)
		sc.observeTurn(t)
	}
	if reason := sc.exceeded(usage); reason != "" {
		t.Fatalf("cap tripped on the re-sent prefix: %q", reason)
	}
	if want := int64(400 + 4*100 + 5*50); sc.requestCharged != want {
		t.Fatalf("charged %d, want %d (new context + completions only)", sc.requestCharged, want)
	}
	// The warning threshold reads the same charge.
	if !sc.nearRequestLimit(usage) {
		t.Fatalf("nearRequestLimit false at %d/1200 charged", sc.requestCharged)
	}
	// One more fat turn does trip it.
	fat := turn.TokenUsage{Available: true, LatestPromptTokens: 3000, LatestCompletionTokens: 100}
	sc.observeTurn(fat)
	if reason := sc.exceeded(usage.AccumulateTurnUsage(fat)); reason == "" {
		t.Fatalf("cap did not trip after %d charged tokens", sc.requestCharged)
	}
}

// A best-effort answer over an unfinished tree reads as a confident
// completion, so the step limit has to be visible in the answer and,
// machine-readably, in the response the ledger is written from (ADR 0001).
func TestStepLimitMarksTheAnswerAndTheStopReason(t *testing.T) {
	runner, _ := budgetRunner(t, config.AgentConfig{MaxTurns: 2}, 2, turn.TokenUsage{})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.StopReason != stopReasonStepLimit {
		t.Fatalf("stop reason = %q, want %q", resp.StopReason, stopReasonStepLimit)
	}
	if !strings.HasPrefix(resp.Message, "⚠ step limit reached (2 turns): work may be incomplete") {
		t.Fatalf("answer must open with the step-limit notice, got %q", resp.Message)
	}
}

// The repeat-loop give-up is not a budget running out — the work is usually
// already done — so it keeps its plain summary and its own stop reason.
func TestRepeatLoopStopReasonSkipsTheIncompleteNotice(t *testing.T) {
	runner, _ := budgetRunner(t, config.AgentConfig{MaxTurns: 40}, 40, turn.TokenUsage{})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.StopReason != stopReasonRepeatLoop {
		t.Fatalf("stop reason = %q, want %q", resp.StopReason, stopReasonRepeatLoop)
	}
	if strings.Contains(resp.Message, "work may be incomplete") {
		t.Fatalf("the repeat path must not claim the work is incomplete: %q", resp.Message)
	}
}

// TestRequestCapWarnsBeforeItTrips covers #496: the cap is tested before the
// warning each turn, so on actual spend a run whose turns are fat relative to
// the remaining budget crosses 85% and 100% in the same turn and never gets its
// wrap-up turn. A marathon run ended that way with a non-compiling tree.
func TestRequestCapWarnsBeforeItTrips(t *testing.T) {
	// 400 charged per turn against a 1000 cap: turn 1 lands at 40%, turn 2 at
	// 80% — never in the 85–100% band — and turn 3 is over the cap.
	usage := turn.TokenUsage{Available: true, LatestCompletionTokens: 400, TurnCompletionTokens: 400}
	cfg := config.AgentConfig{MaxTurns: 40, MaxRequestTokens: 1000}
	runner, gateway := budgetRunner(t, cfg, 40, usage)

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	warned := false
	for _, req := range gateway.continueRequests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "Budget almost spent") {
				warned = true
			}
		}
	}
	if !warned {
		t.Fatalf("no wrap-up warning reached the model before the request cap tripped")
	}
}
