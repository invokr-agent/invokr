package agent

import (
	"context"
	"testing"

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
