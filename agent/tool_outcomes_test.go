package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

type sequenceGateway struct {
	turns            []ModelTurn
	next             int
	continueRequests []ContinueModelRequest
}

func (g *sequenceGateway) Begin(context.Context, BeginModelRequest) (ModelTurn, error) {
	return g.take()
}

func (g *sequenceGateway) Continue(_ context.Context, req ContinueModelRequest) (ModelTurn, error) {
	// Record a copy: the runner appends to the same backing array on later
	// turns, which would rewrite what this call was handed.
	recorded := req
	recorded.Messages = append([]inference.Message{}, req.Messages...)
	g.continueRequests = append(g.continueRequests, recorded)
	turn, err := g.take()
	turn.Messages = req.Messages
	return turn, err
}

func (g *sequenceGateway) take() (ModelTurn, error) {
	if g.next >= len(g.turns) {
		return ModelTurn{}, errors.New("no model turn")
	}
	turn := g.turns[g.next]
	g.next++
	return turn, nil
}

func TestRunnerRetainsFailedAttemptAfterLaterSuccess(t *testing.T) {
	calls := 0
	runner := testRunner(t, func(context.Context, *core.ToolCall) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("first action failed")
		}
		return "ok", nil
	}, []string{
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`{"type":"reply","message":"done"}`,
	})
	var afterResponse events.Event
	unregister := runner.events.(*events.OrderedDispatcher).Register(events.HandlerFunc(func(_ context.Context, event events.Event) error {
		if event.Type == events.EventAfterResponse {
			afterResponse = event
		}
		return nil
	}))
	defer unregister()

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolCallCount != 2 {
		t.Fatalf("ToolCallCount = %d, want 2", resp.ToolCallCount)
	}
	if len(resp.ToolFailures) != 1 || resp.ToolFailures[0].Detail != "first action failed" {
		t.Fatalf("ToolFailures = %#v", resp.ToolFailures)
	}
	guarded := turn.AppendToolOutcomeGuard(resp.Message, resp.ToolFailures)
	if !strings.Contains(guarded, "[Authoritative tool outcome]") {
		t.Fatalf("guard missing: %q", guarded)
	}
	if afterResponse.Response == nil || afterResponse.Response.Text != "done" || len(afterResponse.Response.ToolOutcomes) != 1 {
		t.Fatalf("after-response payload = %#v, want clean text and structured outcome", afterResponse.Response)
	}
	if strings.Contains(afterResponse.Response.Text, "Authoritative tool outcome") {
		t.Fatalf("presentation guard leaked into event text: %q", afterResponse.Response.Text)
	}
}

func TestRunnerClassifiesDeniedToolAttempt(t *testing.T) {
	runner := testRunner(t, func(ctx context.Context, _ *core.ToolCall) (string, error) {
		err := core.EnforceApproval(ctx, approval.StaticApprover{Decision: approval.Decision{Approved: false}}, core.ToolSpec{Name: "demo"}, core.Ask(approval.Request{Tool: "demo"}))
		return "", err
	}, []string{
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`{"type":"reply","message":"denied"}`,
	})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolFailures) != 1 || resp.ToolFailures[0].Kind != "denied" {
		t.Fatalf("ToolFailures = %#v", resp.ToolFailures)
	}
}

func TestNativeParseFailureLedger(t *testing.T) {
	tests := []struct {
		name, tool, wantKind, wantTool string
		wantCalls                      int
	}{
		{name: "named call is attempted tool error", tool: "demo", wantKind: turn.OutcomeKindError, wantTool: "demo", wantCalls: 1},
		{name: "unnamed payload is parse error", wantKind: turn.OutcomeKindParseError, wantTool: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := &TurnProcessor{events: events.NewOrderedDispatcher(zap.NewNop()), syntax: syntax.NewTaggedJSONSyntax(), logger: zap.NewNop()}
			result, _, err := processor.Process(context.Background(), 0, ModelTurn{Completion: completionOutput{toolCalls: []inference.ToolCall{{
				ID: "call-1", Name: tt.tool, Arguments: `{not-json}`,
			}}}})
			if err != nil {
				t.Fatal(err)
			}
			if result.toolCallCount != tt.wantCalls || len(result.outcomes) != 1 || result.outcomes[0].Kind != tt.wantKind || result.outcomes[0].Tool != tt.wantTool {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestOrphanedToolCallMarkupDoesNotFinishTurn(t *testing.T) {
	processor := &TurnProcessor{events: events.NewOrderedDispatcher(zap.NewNop()), syntax: syntax.NewTaggedJSONSyntax(), logger: zap.NewNop()}
	content := "I'll read the file.\n<tool_call>\n<function=read_file>\n<parameter=path>x.go</parameter>\n</function>\n</tool_call>"
	result, _, err := processor.Process(context.Background(), 0, ModelTurn{Completion: completionOutput{content: content}})
	if err != nil {
		t.Fatal(err)
	}
	if result.done {
		t.Fatalf("turn finished despite orphaned tool-call markup: %#v", result)
	}
	if len(result.outcomes) != 1 || result.outcomes[0].Kind != "parse-error" || result.toolCallCount != 0 {
		t.Fatalf("outcomes = %#v", result.outcomes)
	}
}

func TestToolCallMarkupInCodeFenceFinishesTurn(t *testing.T) {
	reply := "Example:\n```xml\n<tool_call>\n<function=read_file>\n</tool_call>\n```"
	if containsOrphanedToolCallMarkup(reply) {
		t.Fatal("tool-call documentation in code fence treated as orphaned call")
	}
}

func TestRunnerReportsZeroToolCalls(t *testing.T) {
	runner := testRunner(t, nil, []string{`{"type":"reply","message":"done"}`})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolCallCount != 0 || len(resp.ToolFailures) != 0 {
		t.Fatalf("response = %#v", resp)
	}
}

func TestSyntheticParseFailureIsRecorded(t *testing.T) {
	processor := &TurnProcessor{events: events.NewOrderedDispatcher(zap.NewNop()), syntax: syntax.NewTaggedJSONSyntax(), logger: zap.NewNop()}
	result, _, err := processor.Process(context.Background(), 0, ModelTurn{Completion: completionOutput{content: toolCallFailedAnswer, syntheticFailure: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.done || result.answer != toolCallFailedAnswer || result.toolCallCount != 0 || len(result.outcomes) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if outcome := result.outcomes[0]; outcome.Tool != "unknown" || outcome.Kind != "parse-error" || outcome.Detail != "tool call arguments unparseable after retry" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestParseRecoveryContinuesTheTurnLoop(t *testing.T) {
	processor := &TurnProcessor{events: events.NewOrderedDispatcher(zap.NewNop()), syntax: syntax.NewTaggedJSONSyntax(), logger: zap.NewNop()}
	completion := completionOutput{parseRecovered: true, parseDetail: "Failed to parse tool call arguments as JSON"}
	result, messages, err := processor.Process(context.Background(), 0, ModelTurn{Completion: completion})
	if err != nil {
		t.Fatal(err)
	}
	if result.done {
		t.Fatalf("thread ended on a recoverable parse failure: %#v", result)
	}
	if len(result.outcomes) != 1 || result.outcomes[0].Tool != "unknown" || result.outcomes[0].Kind != "parse-error" {
		t.Fatalf("outcomes = %#v", result.outcomes)
	}
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "truncated or unparseable") {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestRunnerSubstitutesEmptyModelResponse(t *testing.T) {
	runner := testRunner(t, nil, []string{""})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, "The model returned an empty response") {
		t.Fatalf("Message = %q, want the empty-response fallback", resp.Message)
	}
	// The reason distinguishes a spent turn budget from a provider that just
	// stopped, instead of leaving both as the same bare sentence.
	if !strings.Contains(resp.Message, "turn budget is spent: 1 of 1") {
		t.Fatalf("Message = %q, want the spent turn budget named", resp.Message)
	}
}

// A blank final on the last allowed turn is reported as the turn budget
// running out, not as an unexplained empty response.
func TestEmptyAnswerReasonNamesTheTurnBudget(t *testing.T) {
	if got := emptyAnswerReason(11, 12, ""); !strings.Contains(got, "12 of 12 turns used") {
		t.Fatalf("reason = %q, want the spent turn budget", got)
	}
	if got := emptyAnswerReason(2, 12, "stop"); !strings.Contains(got, `finished with "stop"`) {
		t.Fatalf("reason = %q, want the provider finish reason", got)
	}
}

func testRunner(t *testing.T, handler core.NativeHandler, outputs []string) *Runner {
	t.Helper()
	caller := &tools.Caller{}
	if handler != nil {
		caller.RegisterNative(core.ToolSpec{Name: "demo", Metadata: core.ToolMetadata{AlwaysVisible: true}}, handler)
	}
	turns := make([]ModelTurn, 0, len(outputs))
	for _, output := range outputs {
		turns = append(turns, ModelTurn{Completion: completionOutput{content: output}})
	}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	return &Runner{
		modelGateway: &sequenceGateway{turns: turns},
		turnProcessor: &TurnProcessor{
			events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop(),
		},
		events: bus, settings: config.AgentConfig{MaxTurns: len(outputs)}, logger: zap.NewNop(), usage: turn.NewSessionTokenUsage(0),
	}
}

func TestRunnerPersistsUsageWhenTurnFails(t *testing.T) {
	runner := testRunner(t, func(context.Context, *core.ToolCall) (string, error) {
		return "ok", nil
	}, []string{`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`})
	runner.settings.MaxTurns = 3
	gateway := runner.modelGateway.(*sequenceGateway)
	gateway.turns[0].Completion.usage = turn.TokenUsage{Available: true, TurnPromptTokens: 7, TurnCompletionTokens: 3, TurnTotalTokens: 10}

	// Turn 2 errors (gateway exhausted); the failed request's usage must still
	// land in the runner's snapshot.
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"}); err == nil {
		t.Fatal("expected run to fail when the gateway is exhausted")
	}
	if runner.usage.SessionTotalTokens != 10 {
		t.Fatalf("runner usage SessionTotalTokens = %d, want 10", runner.usage.SessionTotalTokens)
	}
}

func TestRunnerDoesNotPersistRetryNudgeAtTurnLimit(t *testing.T) {
	runner := testRunner(t, func(context.Context, *core.ToolCall) (string, error) {
		return "same result", nil
	}, []string{
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`{"type":"reply","message":"best effort"}`,
	})
	runner.settings.MaxTurns = 3

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"}); err != nil {
		t.Fatal(err)
	}
	gateway := runner.modelGateway.(*sequenceGateway)
	lastRequest := gateway.continueRequests[len(gateway.continueRequests)-1]
	if !lastRequest.DisableTools {
		t.Fatal("last request should be graceful exhaustion")
	}
	// The salvage call carries the wrap-up instruction (#444) as its last
	// message, and nothing internal before it.
	if last := lastRequest.Messages[len(lastRequest.Messages)-1]; last.Content != exhaustionWrapUp {
		t.Fatalf("last salvage message = %q, want the wrap-up instruction", last.Content)
	}
	for _, message := range append(lastRequest.Messages[:len(lastRequest.Messages)-1], runner.messages...) {
		if strings.HasPrefix(message.Content, internalNudgePrefix) {
			t.Fatalf("internal retry nudge leaked into history: %q", message.Content)
		}
	}
}
