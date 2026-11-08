package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/turn"
)

// The second ask reports the grown limit, not the one just spent.
func TestContinueGateGrantsAnotherBatch(t *testing.T) {
	asked := []string{}
	gate := func(_ context.Context, reason string) bool {
		asked = append(asked, reason)
		return len(asked) == 1
	}
	runner, _ := budgetRunner(t, config.AgentConfig{MaxTurns: 2, TurnExtension: 0, HardTurnMultiplier: 2}, 4, turn.TokenUsage{})

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go", ContinueGate: gate}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(asked) != 2 || !strings.Contains(asked[0], "turn budget spent (2/2") || !strings.Contains(asked[1], "(4/4") {
		t.Fatalf("gate asked with %v, want one ask at 2/2 and one at 4/4", asked)
	}
}

// A denial answers exactly as no gate at all did.
func TestContinueGateDeniedKeepsWrapUp(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 2, TurnExtension: 0, HardTurnMultiplier: 2}

	var denied, ungated string
	for _, gate := range []turn.ContinueGate{nil, func(_ context.Context, _ string) bool { return false }} {
		runner, _ := budgetRunner(t, cfg, 4, turn.TokenUsage{})
		resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go", ContinueGate: gate})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if gate == nil {
			ungated = resp.Message
		} else {
			denied = resp.Message
		}
		if !strings.Contains(resp.Message, "step limit reached (2 turns)") {
			t.Fatalf("gate %v answer = %q, want the two-turn wrap-up", gate != nil, resp.Message)
		}
	}
	if denied != ungated {
		t.Fatalf("a denied ask changed the answer: %q vs %q", denied, ungated)
	}
}

// TestGrantRaisesTheHardCeiling: the ceiling bounds a zero-cost run's raw loop,
// so a grant that left it alone would cancel itself one turn later.
func TestGrantRaisesTheHardCeiling(t *testing.T) {
	budget := newTurnBudget(config.AgentConfig{MaxTurns: 2, TurnExtension: 0, HardTurnMultiplier: 2}, nil)
	before, limitBefore := budget.hardCeiling, budget.limit
	budget.grant()
	if budget.hardCeiling != before+2 || budget.limit != limitBefore+2 || budget.batch != 2 {
		t.Fatalf("after grant: ceiling %d limit %.0f batch %d, want both up by 2", budget.hardCeiling, budget.limit, budget.batch)
	}
}
