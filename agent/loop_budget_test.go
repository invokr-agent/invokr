package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/turn"

	inference "github.com/rivt-ai/go-inference-router"
)

// The round-5 grind: one script re-run with a constant changed each turn. The
// exact-signature guard never fires; the near-duplicate guard does at
// maxNearDuplicateCalls.
func TestRepeatGuardDetectsNearDuplicateCalls(t *testing.T) {
	var guard repeatGuard
	script := func(i int) string {
		return fmt.Sprintf(`{"code":"from itertools import permutations\nguests = [\"Alice\",\"Bob\",\"Charlie\",\"Dana\"]\nfor p in permutations(guests):\n    if p[%d] == \"Bob\" and p[2] != \"Dana\":\n        print(p)\n        break\n"}`, i)
	}
	for i := 0; i < maxNearDuplicateCalls-1; i++ {
		if guard.observeNearDuplicate([]toolCallSignature{{Name: "code_interpreter", Key: fmt.Sprint(i), Args: script(i)}}) {
			t.Fatalf("near-duplicate guard fired early at %d", i)
		}
	}
	if !guard.observeNearDuplicate([]toolCallSignature{{Name: "code_interpreter", Key: "last", Args: script(9)}}) {
		t.Fatal("near-duplicate streak was not detected")
	}

	// A different tool, a genuinely different script, or a multi-call turn
	// breaks the streak.
	var reset repeatGuard
	for i := 0; i < maxNearDuplicateCalls-1; i++ {
		reset.observeNearDuplicate([]toolCallSignature{{Name: "code_interpreter", Args: script(i)}})
	}
	reset.observeNearDuplicate([]toolCallSignature{{Name: "code_interpreter", Args: `{"code":"import json\nprint(json.dumps(open(\"seats.json\").read()))\n"}`}})
	if reset.similarCalls != 1 {
		t.Fatalf("different script did not reset the streak: %d", reset.similarCalls)
	}
	reset.observeNearDuplicate([]toolCallSignature{{Name: "shell", Args: "ls"}, {Name: "shell", Args: "pwd"}})
	if reset.similarCalls != 0 {
		t.Fatalf("multi-call turn did not clear the streak: %d", reset.similarCalls)
	}
}

func TestBigramJaccard(t *testing.T) {
	a := `{"code":"import json\nrows = json.load(open(\"seats.json\"))\nfor row in rows:\n    if row[\"table\"] == 1 and row[\"seat\"] != 4:\n        print(row[\"name\"])\n"}`
	if got := bigramJaccard(a, a); got != 1 {
		t.Fatalf("identical = %v, want 1", got)
	}
	b := strings.Replace(a, `== 1 and`, `== 2 and`, 1)
	if got := bigramJaccard(a, b); got < nearDuplicateSimilarity {
		t.Fatalf("one constant changed = %v, want >= %v", got, nearDuplicateSimilarity)
	}
	if got := bigramJaccard(a, `{"code":"import os\nprint(sorted(os.listdir(\"deps\")))\n"}`); got >= 0.5 {
		t.Fatalf("different script = %v, want well under threshold", got)
	}
	// Sibling paths the model reads in sequence are not a grind; the
	// threshold must sit above them (the PR that added the guard measured
	// 0.89 and 0.86 for these two).
	for _, pair := range [][2]string{
		{`{"path":"tests/fixtures/case_01.json"}`, `{"path":"tests/fixtures/case_02.json"}`},
		{`{"path":"internal/user_repository_impl.go"}`, `{"path":"internal/order_repository_impl.go"}`},
	} {
		if got := bigramJaccard(pair[0], pair[1]); got >= nearDuplicateSimilarity {
			t.Fatalf("sibling paths %q vs %q = %v, want under %v", pair[0], pair[1], got, nearDuplicateSimilarity)
		}
	}
	if got := bigramJaccard("", ""); got != 1 {
		t.Fatalf("empty/empty = %v, want 1", got)
	}
}

// A near-duplicate streak is nudged with the tool named, and the nudge is not
// kept in the stored transcript.
func TestNearDuplicateStreakNudgesWithToolName(t *testing.T) {
	turns := make([]ModelTurn, 0, maxNearDuplicateCalls+1)
	for i := range maxNearDuplicateCalls {
		// Script-length arguments, as in the grind: a one-line payload is
		// too short for one changed digit to stay above the threshold.
		call := fmt.Sprintf(`<|tool_call>{"name":"demo","arguments":{"code":"import json\nrows = json.load(open(\"seats.json\"))\nfor row in rows:\n    if row[\"table\"] == %d and row[\"seat\"] != 4:\n        print(row[\"name\"])\n"}}<|tool_call|>`, i)
		turns = append(turns, ModelTurn{Completion: completionOutput{content: call}})
	}
	turns = append(turns, ModelTurn{Completion: completionOutput{content: `{"type":"reply","message":"done"}`}})
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 20}, 0, turn.TokenUsage{})
	gateway.turns = turns

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	last := gateway.continueRequests[len(gateway.continueRequests)-1]
	tail := last.Messages[len(last.Messages)-1]
	if tail.Role != inference.RoleUser || !strings.Contains(tail.Content, "demo calls were near-identical") {
		t.Fatalf("last message before the reply = %#v, want the near-duplicate nudge", tail)
	}
	for _, msg := range runner.messages {
		if strings.HasPrefix(msg.Content, internalNudgePrefix) {
			t.Fatalf("nudge %q survived into the stored transcript", msg.Content)
		}
	}
}

// A spent wall-clock budget ends the run through the best-effort wrap-up, not
// a hard kill: the answer names the reason and the run still completes.
func TestRunTimeoutEndsRunThroughWrapUp(t *testing.T) {
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 20, RunTimeout: time.Nanosecond}, 5, turn.TokenUsage{})
	gateway.turns = append(gateway.turns[:1], ModelTurn{Completion: completionOutput{content: `{"type":"reply","message":"salvaged"}`}})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasSuffix(resp.Message, "salvaged") {
		t.Fatalf("answer = %q, want the wrap-up answer", resp.Message)
	}
	// The check runs after each turn, so exactly one tool turn and one salvage
	// call reach the gateway.
	if gateway.next != 2 {
		t.Fatalf("gateway turns consumed = %d, want 2 (one tool turn, one salvage)", gateway.next)
	}
	salvage := gateway.continueRequests[len(gateway.continueRequests)-1]
	if !salvage.DisableTools || !strings.Contains(salvage.Messages[len(salvage.Messages)-1].Content, "tools are now disabled") {
		t.Fatalf("salvage request = %#v, want tool-free wrap-up", salvage)
	}
}

func TestRunTimeoutDisabledAtZero(t *testing.T) {
	runner, gateway := budgetRunner(t, config.AgentConfig{MaxTurns: 20}, 3, turn.TokenUsage{})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil || resp.Message != "partial answer" || gateway.next != 4 {
		t.Fatalf("resp = %+v err = %v next = %d, want all 3 tool turns then the reply", resp, err, gateway.next)
	}
}

// blockingGateway honours ctx the way the real gateway does: the first
// completion blocks until ctx is done and returns its error, later ones
// answer from the script. Every ctx it was handed is kept so a test can read
// the deadlines the runner set.
type blockingGateway struct {
	sequenceGateway
	blockFirst bool
	ctxs       []context.Context
}

func (g *blockingGateway) Begin(ctx context.Context, req BeginModelRequest) (ModelTurn, error) {
	g.ctxs = append(g.ctxs, ctx)
	if g.blockFirst && len(g.ctxs) == 1 {
		<-ctx.Done()
		return ModelTurn{}, ctx.Err()
	}
	return g.sequenceGateway.Begin(ctx, req)
}

func (g *blockingGateway) Continue(ctx context.Context, req ContinueModelRequest) (ModelTurn, error) {
	g.ctxs = append(g.ctxs, ctx)
	return g.sequenceGateway.Continue(ctx, req)
}

// A completion that outlives the wall-clock budget is cut at the budget and
// the run still ends through the wrap-up, which runs under its own deadline
// of salvagePercent of RunTimeout.
func TestRunTimeoutCutsCompletionMidTurn(t *testing.T) {
	const budget = 40 * time.Millisecond
	runner, seq := budgetRunner(t, config.AgentConfig{MaxTurns: 20, RunTimeout: budget}, 0, turn.TokenUsage{})
	seq.turns = []ModelTurn{{Completion: completionOutput{content: `{"type":"reply","message":"salvaged"}`}}}
	gateway := &blockingGateway{sequenceGateway: *seq, blockFirst: true}
	runner.modelGateway = gateway

	started := time.Now()
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasSuffix(resp.Message, "salvaged") {
		t.Fatalf("answer = %q, want the wrap-up answer", resp.Message)
	}
	if len(gateway.ctxs) != 2 {
		t.Fatalf("gateway calls = %d, want the cut completion and the salvage", len(gateway.ctxs))
	}
	first, ok := gateway.ctxs[0].Deadline()
	if !ok || first.After(started.Add(budget+10*time.Millisecond)) {
		t.Fatalf("completion deadline = %v ok=%v, want within the budget", first, ok)
	}
	salvage, ok := gateway.ctxs[1].Deadline()
	if !ok || salvage.After(started.Add(budget*(100+salvagePercent)/100+10*time.Millisecond)) {
		t.Fatalf("salvage deadline = %v ok=%v, want within %d%% of the budget", salvage, ok, 100+salvagePercent)
	}
}

// A caller that goes away is still a failure, not a salvage: the budget did
// not fire, so the cut is the caller's and the error propagates.
func TestCancelledCallerIsNotSalvaged(t *testing.T) {
	runner, seq := budgetRunner(t, config.AgentConfig{MaxTurns: 20, RunTimeout: time.Hour}, 0, turn.TokenUsage{})
	gateway := &blockingGateway{sequenceGateway: *seq, blockFirst: true}
	runner.modelGateway = gateway
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Run(ctx, RunRequest{SessionID: "s1", Message: "go"}); err == nil {
		t.Fatal("cancelled run returned no error")
	}
	if len(gateway.ctxs) != 1 {
		t.Fatalf("gateway calls = %d, want no salvage after a caller cancel", len(gateway.ctxs))
	}
}

func TestRunTimeoutZeroLeavesContextUnbounded(t *testing.T) {
	runner, seq := budgetRunner(t, config.AgentConfig{MaxTurns: 20}, 0, turn.TokenUsage{})
	gateway := &blockingGateway{sequenceGateway: *seq}
	runner.modelGateway = gateway
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := gateway.ctxs[0].Deadline(); ok {
		t.Fatal("RunTimeout=0 still put a deadline on the completion")
	}
}

// A run whose tool calls keep coming back unparseable must not buy the
// progress extension on the strength of the one turn that did work before the
// grind started, and the model must be told to make the calls smaller (#490).
func TestConsecutiveParseFailuresBlockExtension(t *testing.T) {
	cfg := config.AgentConfig{MaxTurns: 4, TurnExtension: 10, HardTurnMultiplier: 4}
	runner, gateway := budgetRunner(t, cfg, 0, turn.TokenUsage{})
	// One real call, then nothing but truncated calls the syntax cannot parse.
	malformed := completionOutput{content: `<|tool_call>{"name":"demo","arguments":{"pad":"` + strings.Repeat("x", 200)}
	gateway.turns = []ModelTurn{{Completion: completionOutput{content: paddedToolCall()}}}
	for range 20 {
		gateway.turns = append(gateway.turns, ModelTurn{Completion: malformed})
	}

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "go"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Four budgeted turns plus the tool-free wrap-up: no extension was granted.
	if gateway.next != 5 {
		t.Fatalf("gateway turns consumed = %d, want 5 (the base budget plus the wrap-up)", gateway.next)
	}
	nudged := false
	for _, req := range gateway.continueRequests {
		for _, msg := range req.Messages {
			nudged = nudged || strings.Contains(msg.Content, "could not be parsed; split the edit")
		}
	}
	if !nudged {
		t.Fatal("the model was never steered after two consecutive parse failures")
	}
}
