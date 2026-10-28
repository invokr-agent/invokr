package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/support/strutil"
)

// Exercise the combined path: a changing raw result must not be stopped by
// the near-duplicate guard before the exact-result guard can consider it.
func TestProgressAwareRepeatNudges(t *testing.T) {
	for _, test := range []struct {
		name              string
		changing, failure bool
		wantNudge         bool
	}{
		{"stalled fetch", false, false, true},
		{"polling progress", true, false, false},
		{"nonzero shell with changing diagnostics", true, false, false},
		{"changing create errors", true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, _ := budgetRunner(t, config.AgentConfig{}, 0, turn.TokenUsage{})
			var state runState
			for i := range maxNearDuplicateCalls {
				output := `{"exit_code":1,"stdout":"endpoint unavailable"}`
				if test.changing {
					output = fmt.Sprintf(`{"exit_code":1,"stdout":"%d tests still failing"}`, 10-i)
					if test.name == "polling progress" {
						output = fmt.Sprintf(`{"exit_code":0,"stdout":"%d jobs finished"}`, i)
					}
				}
				args := fmt.Sprintf(`{"command":"curl --max-time 10 https://example.invalid/healthz --retry 0 --output ops/health.out; printf 'attempt %d'"}`, i)
				result := turnResult{calls: []toolCallSignature{{Name: "shell", Key: strutil.HashHex(args), Args: args, Result: strutil.HashHex(output)}}}
				if test.failure {
					result.calls[0].Name = "create_tool"
					result.outcomes = []turn.ToolOutcome{{Tool: "create_tool", Kind: turn.OutcomeKindError, Detail: fmt.Sprintf("creation failed %d", i)}}
				}
				if reason, stop := runner.nudgeRepeat(context.Background(), RunRequest{}, i, &state, result); stop {
					t.Fatalf("premature stop: %s", reason)
				}
			}
			if got := state.nudges > 0; got != test.wantNudge {
				t.Fatalf("nudged=%v want %v", got, test.wantNudge)
			}
		})
	}
}

func TestNearDuplicateChangedOutputResetsStreak(t *testing.T) {
	var guard repeatGuard
	for i := range maxNearDuplicateCalls - 1 {
		if guard.observeNearDuplicate([]toolCallSignature{{Name: "shell", Args: strings.Repeat("x", 100) + fmt.Sprint(i), Result: "old"}}) {
			t.Fatal("early nudge")
		}
	}
	if guard.observeNearDuplicate([]toolCallSignature{{Name: "shell", Args: strings.Repeat("x", 100) + "5", Result: "new"}}) {
		t.Fatal("progress nudged")
	}
	if guard.similarCalls != 1 {
		t.Fatalf("streak=%d", guard.similarCalls)
	}
}
