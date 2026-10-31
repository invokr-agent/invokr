package agent

import (
	"context"
	"slices"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"

	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/tools/core"
)

// A message sent while a tool runs reaches the model on its next call in the
// same run, and is recorded with the turn.
func TestRunnerTakesSteeredMessageAfterToolRound(t *testing.T) {
	var runner *Runner
	session := func() *Session { return &Session{runner: runner} }
	steerDuringTool := func(context.Context, *core.ToolCall) (string, error) {
		session().Steer("also check the README")
		return "ok", nil
	}
	runner = testRunner(t, steerDuringTool, []string{
		demoCallWithPath,
		`{"type":"reply","message":"Checked both."}`,
	})
	var steered []string
	var recorded *events.ResponsePayload
	runner.events.(*events.OrderedDispatcher).Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		switch ev.Type {
		case events.EventSteered:
			steered = append(steered, ev.Notice.Text)
		case events.EventAfterResponse:
			recorded = ev.Response
		}
		return nil
	}))
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "check notes/todo.md"}); err != nil {
		t.Fatal(err)
	}
	gateway := runner.modelGateway.(*sequenceGateway)
	next := gateway.continueRequests[0].Messages
	// The budget warning nudge stays last, so dropping it after the turn
	// cannot take the user's message with it.
	if got := next[len(next)-2]; got.Role != "user" || got.Content != "also check the README" {
		t.Fatalf("next model call has %#v before its nudge, want the steered message", got)
	}
	if !slices.ContainsFunc(runner.messages, func(m inference.Message) bool { return m.Content == "also check the README" }) {
		t.Fatal("the steered message did not survive in the run's history")
	}
	if !slices.Equal(steered, []string{"also check the README"}) {
		t.Fatalf("steered events = %v", steered)
	}
	if recorded == nil || !slices.Equal(recorded.Steered, []string{"also check the README"}) {
		t.Fatalf("after-response record = %+v, want the steered message", recorded)
	}
}

// Unsteer takes back only what no run has taken; DropSteered clears the rest.
func TestUnsteerAndDrop(t *testing.T) {
	s := &Session{runner: testRunner(t, okHandler, nil)}
	s.Steer("first")
	s.Steer("late")
	if text, ok := s.Unsteer(); !ok || text != "late" {
		t.Fatalf("Unsteer = %q, %v; want the newest message", text, ok)
	}
	s.DropSteered()
	if _, ok := s.Unsteer(); ok {
		t.Fatal("DropSteered left a message")
	}
}

// A user's message that happens to start like an internal nudge is not one.
func TestStripTrailingNudgeKeepsSteeredMessage(t *testing.T) {
	own := internalNudgePrefix + "my words"
	messages := []inference.Message{inference.UserMessage("task"), inference.UserMessage(own)}
	if got := stripTrailingInternalNudge(messages, []string{own}); len(got) != 2 {
		t.Fatalf("stripped the user's steered message: %#v", got)
	}
	if got := stripTrailingInternalNudge(messages, nil); len(got) != 1 {
		t.Fatalf("kept a trailing nudge: %#v", got)
	}
}
