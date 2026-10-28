package agent

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	turnpkg "github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// overflowGateway rejects the first failFirst completion requests with a
// provider context-length error, then answers normally.
type overflowGateway struct {
	failFirst int
	calls     int
	failure   *inference.Error
}

func (g *overflowGateway) Begin(context.Context, BeginModelRequest) (ModelTurn, error) {
	return g.turn()
}

func (g *overflowGateway) Continue(_ context.Context, req ContinueModelRequest) (ModelTurn, error) {
	t, err := g.turn()
	t.Messages = req.Messages
	return t, err
}

func (g *overflowGateway) turn() (ModelTurn, error) {
	g.calls++
	if g.calls <= g.failFirst {
		if g.failure != nil {
			return ModelTurn{}, g.failure
		}
		return ModelTurn{}, &inference.Error{Kind: inference.KindInvalidRequest, Status: 400, Message: "this model's maximum context length is 8192 tokens"}
	}
	return ModelTurn{Completion: completionOutput{content: `{"type":"reply","message":"done"}`}}, nil
}

func overflowRunner(gateway ModelGateway) *Runner {
	bus := events.NewOrderedDispatcher(zap.NewNop())
	return &Runner{
		modelGateway: gateway,
		budget:       &contextBudget{logger: zap.NewNop()},
		turnProcessor: &TurnProcessor{
			events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: &tools.Caller{}, logger: zap.NewNop(),
		},
		events: bus, settings: config.AgentConfig{MaxTurns: 3}, logger: zap.NewNop(), usage: turnpkg.NewSessionTokenUsage(0),
	}
}

func TestRunnerRecoversFromContextOverflowWithOneCompactedRetry(t *testing.T) {
	gateway := &overflowGateway{failFirst: 1}
	runner := overflowRunner(gateway)

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.Message != "done" {
		t.Fatalf("Message = %q, want %q", resp.Message, "done")
	}
	if gateway.calls != 2 {
		t.Fatalf("model called %d times, want 2 (original + one retry)", gateway.calls)
	}
	if !runner.budget.forceCompaction.Load() {
		t.Fatal("recovery did not request a forced compaction")
	}
}

func TestRunnerFailsWhenStillOversizedAfterCompaction(t *testing.T) {
	gateway := &overflowGateway{failFirst: 2}
	runner := overflowRunner(gateway)

	_, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err == nil {
		t.Fatal("want the provider error after the single retry, got nil")
	}
	if !clients.IsContextLengthExceeded(err) {
		t.Fatalf("err = %v, want a context-length-exceeded classification", err)
	}
	if gateway.calls != 2 {
		t.Fatalf("model called %d times, want exactly 2 (retry once, no loop)", gateway.calls)
	}
}

func TestRunnerRetriesStreamedContextOverflowOnlyOnce(t *testing.T) {
	for _, failures := range []int{1, 2} {
		gateway := &overflowGateway{failFirst: failures, failure: &inference.Error{Kind: inference.KindUnavailable, Status: 500, Message: "server_error: Context size has been exceeded."}}
		runner := overflowRunner(gateway)
		resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
		if gateway.calls != 2 || !runner.budget.forceCompaction.Load() {
			t.Fatalf("failures=%d: calls=%d compaction=%v, want one compacted retry", failures, gateway.calls, runner.budget.forceCompaction.Load())
		}
		if failures == 1 && (err != nil || resp.Message != "done") {
			t.Fatalf("expected recovery: response=%+v err=%v", resp, err)
		}
		if failures == 2 && !clients.IsContextLengthExceeded(err) {
			t.Fatalf("expected original overflow after bounded retry, got %v", err)
		}
	}
}
