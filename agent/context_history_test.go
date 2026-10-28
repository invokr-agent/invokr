package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/platform/events"
	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

func exchange(id, tool, args, result string) []inference.Message {
	return []inference.Message{
		inference.AssistantMessage("", inference.ToolCall{ID: id, Name: tool, Arguments: args}),
		inference.ToolMessage(id, result),
	}
}

func TestPreparePreservesOldResultsBelowCompactionThreshold(t *testing.T) {
	for _, window := range []int64{0, 32 << 10, 64 << 10} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			var notices []events.Event
			bus := events.NewOrderedDispatcher(zap.NewNop(), events.HandlerFunc(func(_ context.Context, e events.Event) error {
				notices = append(notices, e)
				return nil
			}))
			budget := &contextBudget{windowSize: window, events: bus, logger: zap.NewNop()}
			messages := replayPrefix()
			for i := range 40 {
				messages = append(messages, exchange(fmt.Sprint(i), readFileToolName, `{"path":"same.go"}`, strings.Repeat("old file content\n", 40))...)
			}
			original := append([]inference.Message(nil), messages...)
			state := contextWindowState{tokenEstimateScale: 1}
			got, next := budget.Prepare(context.Background(), "s", messages, state)
			if !reflect.DeepEqual(got, original) || next != state || len(notices) != 0 {
				t.Fatalf("ordinary turns must preserve history and state without notices: %d notices", len(notices))
			}
		})
	}
}
