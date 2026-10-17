package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	inference "github.com/rivt-ai/go-inference-router"
)

func TestRunTimeoutBoundsToolAndKeepsSalvageLive(t *testing.T) {
	const budget = 40 * time.Millisecond
	runner, seq := budgetRunner(t, config.AgentConfig{MaxTurns: 10, RunTimeout: budget}, 1, turn.TokenUsage{})
	gateway := &blockingGateway{sequenceGateway: *seq}
	runner.modelGateway = gateway
	var toolDeadline time.Time
	runner.turnProcessor.tools.(*tools.Caller).RegisterNative(core.ToolSpec{Name: "demo"}, func(ctx context.Context, _ *core.ToolCall) (string, error) {
		toolDeadline, _ = ctx.Deadline()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
			return "tool overran budget", nil
		}
	})
	started := time.Now()
	response, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run slow tool"})
	if err != nil {
		t.Fatal(err)
	}
	if toolDeadline.IsZero() || toolDeadline.After(started.Add(budget+10*time.Millisecond)) {
		t.Fatalf("tool deadline=%v; want run deadline", toolDeadline)
	}
	if response.StopReason != stopReasonTimeBudget || !strings.Contains(response.Message, "partial answer") {
		t.Fatalf("response=%+v", response)
	}
	if response.ToolCallCount != 1 || len(response.ToolFailures) != 1 {
		t.Fatalf("lost tool outcome: %+v", response)
	}
	if len(gateway.ctxs) != 2 {
		t.Fatalf("model calls=%d, want work and salvage", len(gateway.ctxs))
	}
	salvageDeadline, ok := gateway.ctxs[1].Deadline()
	if !ok || !salvageDeadline.After(toolDeadline) {
		t.Fatalf("salvage reused expired work deadline: %v", salvageDeadline)
	}
}

func TestCallerCancellationDuringToolDoesNotSalvage(t *testing.T) {
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 10, RunTimeout: time.Hour}, 1, turn.TokenUsage{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.turnProcessor.tools.(*tools.Caller).RegisterNative(core.ToolSpec{Name: "demo"}, func(ctx context.Context, _ *core.ToolCall) (string, error) { cancel(); return "", ctx.Err() })
	_, err := runner.Run(ctx, RunRequest{SessionID: "s1", Message: "cancel tool"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want caller cancellation", err)
	}
	if gateway.next != 1 {
		t.Fatalf("model calls=%d; caller cancellation must not invoke salvage", gateway.next)
	}
}

type deadlineSummaryGateway struct {
	*blockingGateway
	summaryDeadline time.Time
}

func (g *deadlineSummaryGateway) SummarizeConversation(ctx context.Context, _ []inference.Message) (string, error) {
	g.summaryDeadline, _ = ctx.Deadline()
	return "Earlier work completed.", nil
}

func TestSalvageCompactionUsesSalvageDeadline(t *testing.T) {
	const timeout = time.Second
	runner, seq := budgetRunner(t, config.AgentConfig{MaxTurns: 10, RunTimeout: timeout}, 0, turn.TokenUsage{})
	gateway := &deadlineSummaryGateway{blockingGateway: &blockingGateway{sequenceGateway: *seq}}
	runner.modelGateway = gateway
	runner.budget = &contextBudget{windowSize: 100000, gateway: gateway, logger: runner.logger, events: runner.events}
	runner.budget.RequestCompaction()
	messages := []inference.Message{inference.UserMessage("task")}
	for range 10 {
		messages = append(messages, inference.AssistantMessage("working"), inference.UserMessage("continue"))
	}
	started := time.Now()
	_, err := runner.gracefulExhaustion(context.Background(), RunRequest{SessionID: "s1"}, runState{messages: messages}, started, "time budget reached")
	if err != nil {
		t.Fatal(err)
	}
	if gateway.summaryDeadline.IsZero() || gateway.summaryDeadline.After(started.Add(timeout*salvagePercent/100+50*time.Millisecond)) {
		t.Fatalf("unbounded salvage compaction: %v", gateway.summaryDeadline)
	}
	deadline, _ := gateway.ctxs[0].Deadline()
	if !deadline.Equal(gateway.summaryDeadline) {
		t.Fatalf("summary and salvage did not share deadline: %v, %v", gateway.summaryDeadline, deadline)
	}
}
