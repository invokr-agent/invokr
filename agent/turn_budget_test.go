package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	turnpkg "github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools/core"
)

func testCaps() map[string]core.ToolCapabilities {
	return map[string]core.ToolCapabilities{
		"demo": {ReadsWorkspace: true},
		"edit": {WritesWorkspace: true},
	}
}

func readOnlyTurn(key string) turnResult {
	return turnResult{toolCallCount: 1, calls: []toolCallSignature{{Name: "demo", Key: key}}}
}

func TestTurnCostWeighsReadDenialAndWrite(t *testing.T) {
	budget := newTurnBudget(config.AgentConfig{MaxTurns: 10, ReadTurnWeight: 0.5, HardTurnMultiplier: 2}, testCaps())
	cases := []struct {
		name   string
		result turnResult
		want   float64
	}{
		{"read only", readOnlyTurn("a"), 0.5},
		{"write", turnResult{toolCallCount: 1, calls: []toolCallSignature{{Name: "edit", Key: "b"}}}, 1},
		{"unknown tool", turnResult{toolCallCount: 1, calls: []toolCallSignature{{Name: "mystery", Key: "c"}}}, 1},
		{"no calls", turnResult{done: true}, 1},
		{"all denied", turnResult{toolCallCount: 1, calls: []toolCallSignature{{Name: "edit", Key: "d"}},
			outcomes: []turnpkg.ToolOutcome{{Tool: "edit", Kind: turnpkg.OutcomeKindDenied}}}, 0},
		{"one of two denied", turnResult{toolCallCount: 2, calls: []toolCallSignature{{Name: "edit", Key: "e"}, {Name: "demo", Key: "f"}},
			outcomes: []turnpkg.ToolOutcome{{Tool: "edit", Kind: turnpkg.OutcomeKindDenied}}}, 1},
	}
	for _, tc := range cases {
		if got := budget.cost(tc.result); got != tc.want {
			t.Fatalf("%s cost = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A run whose every turn is denied costs nothing, so only the raw ceiling ends
// it — the reason the ceiling exists.
func TestHardCeilingStopsZeroCostTurns(t *testing.T) {
	budget := newTurnBudget(config.AgentConfig{MaxTurns: 4, ReadTurnWeight: 0.5, HardTurnMultiplier: 2}, testCaps())
	denied := turnResult{toolCallCount: 1, calls: []toolCallSignature{{Name: "edit", Key: "x"}},
		outcomes: []turnpkg.ToolOutcome{{Tool: "edit", Kind: turnpkg.OutcomeKindDenied}}}
	iterations := 0
	for !budget.exhausted() {
		budget.observe(denied, false)
		iterations++
		if iterations > 100 {
			t.Fatal("zero-cost turns spun past the hard ceiling")
		}
	}
	if iterations != 8 {
		t.Fatalf("iterations = %d, want the 4*2 hard ceiling", iterations)
	}
	if budget.spent != 0 {
		t.Fatalf("spent = %v, want denied turns to cost nothing", budget.spent)
	}
}

// The warning counts down against the budget, not the raw loop: two read turns
// spend one of four.
func TestRemainingCountsTheBudgetNotIterations(t *testing.T) {
	budget := newTurnBudget(config.AgentConfig{MaxTurns: 4, ReadTurnWeight: 0.5, HardTurnMultiplier: 2}, testCaps())
	budget.observe(readOnlyTurn("a"), false)
	budget.observe(readOnlyTurn("b"), false)
	if got := budget.remaining(); got != 3 {
		t.Fatalf("remaining = %d, want 3 of the 4-turn budget", got)
	}
}

// End to end: read-only turns at half price let a MaxTurns=2 run take four
// tool turns before the wrap-up.
func TestReadOnlyTurnsStretchTheBudget(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 2, ReadTurnWeight: 0.5, HardTurnMultiplier: 2}
	runner, gateway := budgetRunner(t, cfg, 10, turnpkg.TokenUsage{})
	runner.toolCaps = testCaps()
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gateway.next != 5 {
		t.Fatalf("gateway turns = %d, want 4 read turns plus the salvage", gateway.next)
	}
}

// Weight 1 with multiplier 1 is the old static ceiling exactly.
func TestStaticWeightsReproduceTheOldCeiling(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 2, ReadTurnWeight: 1, HardTurnMultiplier: 1}
	runner, gateway := budgetRunner(t, cfg, 10, turnpkg.TokenUsage{})
	runner.toolCaps = testCaps()
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gateway.next != 3 {
		t.Fatalf("gateway turns = %d, want 2 turns plus the salvage", gateway.next)
	}
}

func TestExtensionRequiresProgressAndNoNudge(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 2, ReadTurnWeight: 1, HardTurnMultiplier: 4, TurnExtension: 2}
	newCalls := newTurnBudget(cfg, testCaps())
	newCalls.observe(readOnlyTurn("a"), false)
	newCalls.observe(readOnlyTurn("b"), false)
	if !newCalls.extend() || newCalls.limit != 4 {
		t.Fatalf("new calls did not extend: limit = %v", newCalls.limit)
	}
	// The window restarts at the grant, so the same decision cannot be taken
	// twice on one window's evidence.
	if newCalls.extend() {
		t.Fatal("extended twice on one window")
	}

	// The window holds the last TurnExtension turns, so the first (necessarily
	// new) call is pushed out of it before the decision.
	repeated := newTurnBudget(cfg, testCaps())
	for range 3 {
		repeated.observe(readOnlyTurn("a"), false)
	}
	if repeated.extend() {
		t.Fatal("a run re-treading the same call was extended")
	}

	nudged := newTurnBudget(cfg, testCaps())
	nudged.observe(readOnlyTurn("a"), false)
	nudged.observe(readOnlyTurn("b"), true)
	if nudged.extend() {
		t.Fatal("a nudged window was extended")
	}

	disabled := newTurnBudget(config.AgentConfig{MaxTurns: 2, ReadTurnWeight: 1, HardTurnMultiplier: 4}, testCaps())
	disabled.observe(readOnlyTurn("a"), false)
	if disabled.extend() {
		t.Fatal("turn_extension=0 still extended the budget")
	}
}

// A landed edit counts as progress even when the call itself repeats; a
// rejected one does not.
func TestExtensionCountsSuccessfulWrites(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 2, ReadTurnWeight: 1, HardTurnMultiplier: 4, TurnExtension: 2}
	write := turnResult{toolCallCount: 1, calls: []toolCallSignature{{Name: "edit", Key: "w"}}}
	landed := newTurnBudget(cfg, testCaps())
	landed.observe(write, false)
	landed.observe(write, false)
	if !landed.extend() {
		t.Fatal("repeated but landing edits were not counted as progress")
	}

	failed := write
	failed.outcomes = []turnpkg.ToolOutcome{{Tool: "edit", Kind: turnpkg.OutcomeKindError}}
	rejected := newTurnBudget(cfg, testCaps())
	rejected.observe(write, false)
	rejected.observe(failed, false)
	rejected.observe(failed, false)
	if rejected.extend() {
		t.Fatal("a window of failing edits was counted as progress")
	}
}

// End to end: a progressing run past its budget is extended and says so.
func TestProgressExtendsTheRunAndNotifies(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 2, ReadTurnWeight: 1, HardTurnMultiplier: 4, TurnExtension: 2}
	runner, gateway := budgetRunner(t, cfg, 10, turnpkg.TokenUsage{})
	runner.toolCaps = testCaps()
	notices := noticeTexts(t, runner)
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Two turns spend the budget; the calls differ each turn, so the run is
	// extended by two before the wrap-up.
	if gateway.next != 5 {
		t.Fatalf("gateway turns = %d, want 4 turns plus the salvage", gateway.next)
	}
	if !slices.ContainsFunc(*notices, func(msg string) bool { return strings.HasPrefix(msg, "turns.extended: +2") }) {
		t.Fatal("no turns.extended notice was emitted")
	}
}

// An extension re-arms the one-shot wrap-up warning: the model was told a turn
// count against the old limit, so it has to hear the new one.
func TestExtensionReArmsTheExhaustionWarning(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 2, ReadTurnWeight: 1, HardTurnMultiplier: 4, TurnExtension: 2}
	runner, _ := budgetRunner(t, cfg, 10, turnpkg.TokenUsage{})
	runner.toolCaps = testCaps()
	budget := newTurnBudget(cfg, testCaps())
	budget.observe(readOnlyTurn("a"), false)
	budget.observe(readOnlyTurn("b"), false)
	state := runState{warned: true}
	if !runner.turnAvailable(context.Background(), RunRequest{SessionID: "s1"}, &budget, &state) {
		t.Fatal("a progressing run past its budget was not extended")
	}
	if state.warned {
		t.Fatal("the extension left the wrap-up warning disarmed")
	}
	runner.warnNearExhaustion(0, &state, time.Now(), &budget)
	if !state.warned {
		t.Fatal("the warning did not fire again against the extended limit")
	}
}

// The extension never buys turns past the hard ceiling: with multiplier 1 the
// loop still stops at MaxTurns however well the run is doing.
func TestExtensionStopsAtTheHardCeiling(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 2, ReadTurnWeight: 1, HardTurnMultiplier: 1, TurnExtension: 2}
	runner, gateway := budgetRunner(t, cfg, 10, turnpkg.TokenUsage{})
	runner.toolCaps = testCaps()
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gateway.next != 3 {
		t.Fatalf("gateway turns = %d, want the 2-turn ceiling plus the salvage", gateway.next)
	}
}
