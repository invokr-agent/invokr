package agent

import (
	"context"
	"slices"
	"testing"

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
	if last := next[len(next)-1]; last.Role != "user" || last.Content != "also check the README" {
		t.Fatalf("next model call ends with %#v, want the steered message", last)
	}
	if !slices.Equal(steered, []string{"also check the README"}) {
		t.Fatalf("steered events = %v", steered)
	}
	if recorded == nil || !slices.Equal(recorded.Steered, []string{"also check the README"}) {
		t.Fatalf("after-response record = %+v, want the steered message", recorded)
	}
}

// A run that answers without another tool round leaves the message for the
// caller, which sends it as the next prompt.
func TestSteeredMessageWaitsWhenRunEnds(t *testing.T) {
	runner := testRunner(t, okHandler, []string{`{"type":"reply","message":"Done."}`})
	s := &Session{runner: runner}
	s.Steer("late")
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "hi"}); err != nil {
		t.Fatal(err)
	}
	if text, ok := s.Unsteer(); !ok || text != "late" {
		t.Fatalf("Unsteer = %q, %v; want the untaken message", text, ok)
	}
	s.Steer("x")
	s.DropSteered()
	if _, ok := s.Unsteer(); ok {
		t.Fatal("DropSteered left a message")
	}
}
