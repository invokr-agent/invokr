package agent

import (
	"context"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/turn"
	inference "github.com/rivt-ai/go-inference-router"
)

func TestCompactNowReplacesStoredTranscript(t *testing.T) {
	runner, seq := budgetRunner(t, config.AgentConfig{MaxTurns: 10}, 0, turn.TokenUsage{})
	gateway := &deadlineSummaryGateway{blockingGateway: &blockingGateway{sequenceGateway: *seq}}
	runner.budget = &contextBudget{windowSize: 100000, gateway: gateway, logger: runner.logger, events: runner.events}
	runner.messages = []inference.Message{inference.UserMessage("task")}
	for range 10 {
		runner.messages = append(runner.messages, inference.AssistantMessage("working"), inference.UserMessage("continue"))
	}
	before := len(runner.messages)

	runner.compactNow(context.Background(), "s1")

	if len(runner.messages) >= before {
		t.Fatalf("messages = %d, want fewer than %d after an immediate compaction", len(runner.messages), before)
	}
	if runner.budget.forceCompaction.Load() {
		t.Fatal("the request must be consumed, not left for the next turn")
	}
}

// A transcript too short to summarize must not leave the request armed for
// a later turn: that was the original bug in another shape.
func TestCompactNowOnShortTranscriptDoesNotLinger(t *testing.T) {
	runner, seq := budgetRunner(t, config.AgentConfig{MaxTurns: 10}, 0, turn.TokenUsage{})
	gateway := &deadlineSummaryGateway{blockingGateway: &blockingGateway{sequenceGateway: *seq}}
	runner.budget = &contextBudget{windowSize: 100000, gateway: gateway, logger: runner.logger, events: runner.events}
	runner.messages = []inference.Message{inference.UserMessage("hi"), inference.AssistantMessage("hello")}

	runner.compactNow(context.Background(), "s1")

	if runner.budget.forceCompaction.Load() {
		t.Fatal("compaction request left armed for the next turn")
	}
	if len(runner.messages) != 2 {
		t.Fatalf("messages = %d, want the short transcript unchanged", len(runner.messages))
	}
}

type enteredGateway struct {
	*blockingGateway
	entered chan struct{}
}

func (g *enteredGateway) Begin(ctx context.Context, req BeginModelRequest) (ModelTurn, error) {
	if len(g.ctxs) == 0 {
		close(g.entered)
	}
	return g.blockingGateway.Begin(ctx, req)
}

// compactNow must wait for a running turn instead of replacing the transcript
// under it. Run with -race.
func TestCompactNowWaitsForRunningTurn(t *testing.T) {
	runner, seq := budgetRunner(t, config.AgentConfig{MaxTurns: 10}, 0, turn.TokenUsage{})
	gateway := &enteredGateway{blockingGateway: &blockingGateway{sequenceGateway: *seq, blockFirst: true}, entered: make(chan struct{})}
	runner.modelGateway = gateway
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { _, _ = runner.Run(ctx, RunRequest{SessionID: "s1", Message: "task"}); close(ran) }()
	<-gateway.entered

	compacted := make(chan struct{})
	go func() { runner.compactNow(context.Background(), "s1"); close(compacted) }()
	select {
	case <-compacted:
		t.Fatal("compactNow ran while a turn held the transcript")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-ran
	<-compacted
}
