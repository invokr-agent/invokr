package agent

import (
	"context"
	"testing"

	"github.com/invokr-ai/invokr/platform/events"
)

// TestReasoningEventFiresOnDefaultToolCall covers the plain-answer (no tool
// call) turn path: the model's reasoning must reach the events bus so a
// recorded run (exec --json) captures it, not just a debug-log tail (#577).
func TestReasoningEventFiresOnDefaultToolCall(t *testing.T) {
	var captured []events.Event
	p := gateTestProcessor(t, &gateTestCaller{}, &captured)
	_, msgs, err := p.defaultToolCall(context.Background(), 3, nil, "the answer",
		completionOutput{reasoning: "thinking it through"})
	if err != nil {
		t.Fatalf("defaultToolCall: %v", err)
	}
	if msgs[len(msgs)-1].Reasoning != "thinking it through" {
		t.Fatalf("assistant message missing reasoning: %+v", msgs[len(msgs)-1])
	}
	found := false
	for _, e := range captured {
		if e.Type == events.EventReasoning {
			if e.Reasoning == nil || e.Reasoning.Text != "thinking it through" || e.Reasoning.Turn != 3 {
				t.Fatalf("reasoning event payload wrong: %+v", e.Reasoning)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no EventReasoning dispatched: %+v", captured)
	}
}

// TestReasoningEventOmittedWhenEmpty covers the common case (no thinking, or
// a provider that doesn't return any): no event, no noise on the bus.
func TestReasoningEventOmittedWhenEmpty(t *testing.T) {
	var captured []events.Event
	p := gateTestProcessor(t, &gateTestCaller{}, &captured)
	if _, _, err := p.defaultToolCall(context.Background(), 1, nil, "the answer", completionOutput{}); err != nil {
		t.Fatalf("defaultToolCall: %v", err)
	}
	for _, e := range captured {
		if e.Type == events.EventReasoning {
			t.Fatalf("EventReasoning dispatched with no reasoning: %+v", e)
		}
	}
}
