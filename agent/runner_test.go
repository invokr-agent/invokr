package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// historyGateway records every Begin request's History so tests can assert on it.
type historyGateway struct {
	replies    []string
	usages     []turn.TokenUsage
	next       int
	beginCalls []BeginModelRequest
}

func (g *historyGateway) Begin(_ context.Context, req BeginModelRequest) (ModelTurn, error) {
	g.beginCalls = append(g.beginCalls, req)
	messages := []inference.Message{inference.SystemMessage("system prompt")}
	if len(req.History) > 1 {
		messages = append(messages, req.History[1:]...)
	}
	messages = append(messages, inference.UserMessage(req.Message))
	return g.turn(messages)
}

func (g *historyGateway) Continue(_ context.Context, req ContinueModelRequest) (ModelTurn, error) {
	return g.turn(req.Messages)
}

func (g *historyGateway) turn(messages []inference.Message) (ModelTurn, error) {
	reply := g.replies[g.next]
	g.next++
	output := completionOutput{content: `{"type":"reply","message":"` + reply + `"}`}
	if len(g.usages) > g.next-1 {
		output.usage = g.usages[g.next-1]
	}
	return ModelTurn{Messages: messages, Completion: output}, nil
}

func TestRunnerPersistsFinalContextState(t *testing.T) {
	gateway := &historyGateway{replies: []string{"answer"}, usages: []turn.TokenUsage{{Available: true, LatestPromptTokens: 123}}}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	runner := &Runner{modelGateway: gateway, turnProcessor: &TurnProcessor{events: bus, syntax: syntax.NewTaggedJSONSyntax(), tools: &tools.Caller{}, logger: zap.NewNop()}, events: bus, settings: config.AgentConfig{MaxTurns: 1}, logger: zap.NewNop(), usage: turn.NewSessionTokenUsage(0)}
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	if runner.contextState.providerPromptTokens != 123 || runner.contextState.providerMessageTokens == 0 {
		t.Fatalf("context state = %#v", runner.contextState)
	}
}

func TestProviderUsageCalibratesTokenEstimate(t *testing.T) {
	messages := []inference.Message{inference.UserMessage(strings.Repeat("x", 400))}
	state := updateProviderUsage(contextWindowState{}, turn.TokenUsage{Available: true, LatestPromptTokens: 200}, messages, messages)
	if state.tokenEstimateScale < 1.9 || state.tokenEstimateScale > 2.1 {
		t.Fatalf("token estimate scale = %.2f, want about 2", state.tokenEstimateScale)
	}
	if got := contextTokens(append(messages, inference.UserMessage(strings.Repeat("y", 400))), state); got < 390 || got > 410 {
		t.Fatalf("calibrated context tokens = %d, want about 400", got)
	}
}

func TestRepeatGuardDetectsAlternatingAndFailureLoops(t *testing.T) {
	var alternating repeatGuard
	for i, key := range []string{"A", "B", "A"} {
		if alternating.observe(key, "", "") {
			t.Fatalf("alternating loop triggered early at %d", i)
		}
	}
	if !alternating.observe("B", "", "") {
		t.Fatal("A/B/A/B loop was not detected")
	}

	var failures repeatGuard
	for i := 0; i < maxIdenticalToolCallRepeats-1; i++ {
		if failures.observe(fmt.Sprintf("call-%d", i), "shell\x00same error", "shell") {
			t.Fatalf("failure loop triggered early at %d", i)
		}
	}
	if !failures.observe("different-call", "shell\x00same error", "shell") {
		t.Fatal("same tool failure loop was not detected")
	}
}

// A model guessing at a format fails the same tool every turn with a different
// error, so neither the call key nor the error key ever repeats.
func TestRepeatGuardDetectsOneToolFailingWithVaryingErrors(t *testing.T) {
	var guard repeatGuard
	for i := 0; i < maxSameToolFailures-1; i++ {
		key := fmt.Sprintf("apply_patch\x00error-%d", i)
		if guard.observe(fmt.Sprintf("call-%d", i), key, "apply_patch") {
			t.Fatalf("guard fired early at %d", i)
		}
	}
	if !guard.observe("call-last", "apply_patch\x00error-last", "apply_patch") {
		t.Fatal("varying-error loop on one tool was not detected")
	}

	// Rotating between tools keeps every per-tool streak short, so only the
	// consecutive-failed-turns counter catches it.
	var rotating repeatGuard
	tools := []string{"apply_patch", "shell", "read_file"}
	for i := 0; i < maxConsecutiveFailedTurns-1; i++ {
		if rotating.observe(fmt.Sprintf("call-%d", i), "", tools[i%len(tools)]) {
			t.Fatalf("rotation guard fired early at %d", i)
		}
	}
	if !rotating.observe("call-last", "", "shell") {
		t.Fatal("tool-rotating failure loop was not detected")
	}

	// A successful turn in between clears the streak.
	var progressing repeatGuard
	for i := 0; i < maxSameToolFailures*2; i++ {
		failing := ""
		if i%2 == 0 {
			failing = "apply_patch"
		}
		if progressing.observe(fmt.Sprintf("call-%d", i), "", failing) {
			t.Fatalf("guard fired on interleaved successes at %d", i)
		}
	}
}

func TestSessionResumeTranscriptSeedsConversationHistory(t *testing.T) {
	session := &Session{runner: &Runner{}}
	transcript := []turn.TranscriptMessage{{Role: "user", Text: "old question"}, {Role: "assistant", Text: "old answer"}}
	if err := session.ResumeTranscript(transcript); err != nil {
		t.Fatal(err)
	}
	if got := session.runner.messages; len(got) != 3 || got[1].Content != "old question" || got[2].Content != "old answer" {
		t.Fatalf("resumed messages = %#v", got)
	}
	if err := session.ResumeTranscript(transcript); err == nil {
		t.Fatal("second resume succeeded over active history")
	}
}

func TestSessionResumeTranscriptReplaysToolLedger(t *testing.T) {
	session := &Session{runner: &Runner{}}
	transcript := []turn.TranscriptMessage{
		{Role: "user", Text: "old question"},
		{
			Role: "assistant", Text: "old answer",
			ToolCalls:     []turn.ToolOutcome{{Tool: "shell", Kind: turn.OutcomeKindError, Detail: "exit 1"}},
			ToolCallCount: 2,
		},
	}
	if err := session.ResumeTranscript(transcript); err != nil {
		t.Fatal(err)
	}
	got := session.runner.messages[2].Content
	for _, want := range []string{"old answer", "shell", "exit 1", "Tool calls this turn: 2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("resumed assistant message missing %q: %q", want, got)
		}
	}
}

func TestRunnerPersistsHistoryAcrossTurns(t *testing.T) {
	gateway := &historyGateway{replies: []string{"first answer", "second answer"}}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	runner := &Runner{
		modelGateway:  gateway,
		turnProcessor: &TurnProcessor{events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: &tools.Caller{}, logger: zap.NewNop()},
		events:        bus,
		settings:      config.AgentConfig{MaxTurns: 4},
		logger:        zap.NewNop(),
		usage:         turn.NewSessionTokenUsage(0),
	}

	first, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Message != "first answer" {
		t.Fatalf("first.Message = %q", first.Message)
	}

	second, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "follow up"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Message != "second answer" {
		t.Fatalf("second.Message = %q", second.Message)
	}

	if len(gateway.beginCalls) != 2 {
		t.Fatalf("begin calls = %d, want 2", len(gateway.beginCalls))
	}
	history := gateway.beginCalls[1].History
	if len(history) != 3 {
		t.Fatalf("second turn history = %#v, want 3 messages (system, first user, first assistant)", history)
	}
	if history[0].Content != "system prompt" {
		t.Fatalf("history[0] = %#v, want system prompt", history[0])
	}
	if history[1].Content != "hello" {
		t.Fatalf("history[1] = %#v, want first user message", history[1])
	}
	if !strings.Contains(history[2].Content, "first answer") {
		t.Fatalf("history[2] = %#v, want first assistant answer", history[2])
	}
}

func TestStripToolCallMarkupRemovesTextualToolCalls(t *testing.T) {
	markup := "<tool_call>\n<function=read_file>\n<parameter=path>\ndocs/d3.md\n</parameter>\n</function>\n</tool_call>"
	if got := stripToolCallMarkup(markup); got != "" {
		t.Fatalf("bare markup stripped to %q, want empty", got)
	}
	if got := stripToolCallMarkup("Here is what I found.\n" + markup); got != "Here is what I found." {
		t.Fatalf("prose+markup stripped to %q", got)
	}
	if got := stripToolCallMarkup("plain answer"); got != "plain answer" {
		t.Fatalf("plain prose was altered: %q", got)
	}
	if got := stripToolCallMarkup("partial <|tool_call>\n{}"); got != "partial" {
		t.Fatalf("tagged markup stripped to %q", got)
	}
}

func TestRepeatGuardDetectsNonConsecutiveIdenticalCalls(t *testing.T) {
	var guard repeatGuard
	for i, key := range []string{"patch", "other-a", "patch", "other-b", "other-c"} {
		if guard.observe(key, "", "") {
			t.Fatalf("guard fired early at %d (%s)", i, key)
		}
	}
	if !guard.observe("patch", "", "") {
		t.Fatal("third identical call separated by other turns was not detected")
	}
}

// The pending prompt is the last provider count plus everything appended
// since. Measuring the baseline against the gateway's prompt (system prompt
// included) hid that growth, so a single huge tool result went from under the
// warning threshold straight past the window.
func TestContextTokensCountsGrowthSinceLastProviderCount(t *testing.T) {
	sent := []inference.Message{
		inference.SystemMessage(strings.Repeat("s", 4000)),
		inference.UserMessage(strings.Repeat("x", 400)),
	}
	transcript := sent[1:]
	state := updateProviderUsage(contextWindowState{}, turn.TokenUsage{Available: true, LatestPromptTokens: 1100}, sent, transcript)
	grown := append(append([]inference.Message{}, transcript...), inference.ToolMessage("", strings.Repeat("t", 40000)))
	got := contextTokens(grown, state)
	if got < 11000 {
		t.Fatalf("pending prompt estimate = %d, want the 10k-token tool result counted on top of 1100", got)
	}
}
