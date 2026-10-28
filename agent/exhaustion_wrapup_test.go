package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/turn"

	inference "github.com/rivt-ai/go-inference-router"
)

// TestGracefulExhaustionInstructsTheModel covers #444: the salvage completion
// carries an explicit wrap-up instruction, and that instruction is dropped
// from the stored transcript once the model has seen it.
func TestGracefulExhaustionInstructsTheModel(t *testing.T) {
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 2}, 2, turn.TokenUsage{})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasSuffix(resp.Message, "partial answer") {
		t.Fatalf("answer = %q, want the salvaged best-effort answer", resp.Message)
	}
	salvage := gateway.continueRequests[len(gateway.continueRequests)-1]
	if !salvage.DisableTools {
		t.Fatalf("salvage call did not disable tools")
	}
	last := salvage.Messages[len(salvage.Messages)-1]
	if last.Role != inference.RoleUser || !strings.Contains(last.Content, "tools are now disabled") {
		t.Fatalf("last salvage message = %#v, want the wrap-up instruction", last)
	}
	for _, msg := range runner.messages {
		if strings.HasPrefix(msg.Content, internalNudgePrefix) {
			t.Fatalf("internal instruction %q survived into the stored transcript", msg.Content)
		}
	}
}

// TestPreExhaustionWarningIsOneShot covers #444: the model is told once, near
// the turn ceiling, to finish — and the line never reaches history.
func TestPreExhaustionWarningIsOneShot(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: preExhaustionTurns + 2}
	runner, gateway := budgetRunner(t, cfg, cfg.MaxTurns, turn.TokenUsage{})
	gateway.turns = append(gateway.turns, ModelTurn{Completion: completionOutput{content: `{"type":"reply","message":"done"}`}})

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	warned := 0
	for _, req := range gateway.continueRequests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "Budget almost spent") {
				warned++
			}
		}
	}
	if warned != 1 {
		t.Fatalf("warning seen %d times, want exactly one", warned)
	}
	for _, msg := range runner.messages {
		if strings.Contains(msg.Content, "Budget almost spent") {
			t.Fatalf("warning survived into the stored transcript")
		}
	}
}

// TestStripToolCallMarkupDropsThePreamble covers #444: the prose left in front
// of a stripped textual tool call is the call's own preamble, not an answer.
func TestStripToolCallMarkupDropsThePreamble(t *testing.T) {
	cases := map[string]string{
		"I wrote report.json.\nNow let me re-run it:\n<|tool_call>{}": "I wrote report.json.",
		"Now let me write the availability JSON:\n<tool_call>x":       "",
		"Everything passes.\n<|tool_call>{}":                          "Everything passes.",
		"A plain answer with a colon: here it is.":                    "A plain answer with a colon: here it is.",
	}
	for input, want := range cases {
		if got := stripToolCallMarkup(input); got != want {
			t.Fatalf("stripToolCallMarkup(%q) = %q, want %q", input, got, want)
		}
	}
}
