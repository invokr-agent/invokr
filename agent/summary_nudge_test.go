package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/tools/core"
)

const demoCallWithPath = `<|tool_call>{"name":"demo","arguments":{"path":"notes/todo.md"}}<|tool_call|>`

func okHandler(context.Context, *core.ToolCall) (string, error) { return "ok", nil }

func noticeTexts(t *testing.T, runner *Runner) *[]string {
	t.Helper()
	var texts []string
	unregister := runner.events.(*events.OrderedDispatcher).Register(events.HandlerFunc(func(_ context.Context, event events.Event) error {
		if event.Type == events.EventNotice && event.Notice != nil {
			texts = append(texts, event.Notice.Text)
		}
		return nil
	}))
	t.Cleanup(unregister)
	return &texts
}

// A thread that ran tools and then answered with a context-free greeting is
// retried once with the wrap-up nudge, and the second answer is the response.
func TestRunnerNudgesTaskFreeFinalAnswer(t *testing.T) {
	runner := testRunner(t, okHandler, []string{
		demoCallWithPath,
		`{"type":"reply","message":"Hey! What is up? Ready to help whenever you are."}`,
		`{"type":"reply","message":"I updated notes/todo.md with the new items."}`,
	})
	notices := noticeTexts(t, runner)

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, "notes/todo.md") {
		t.Fatalf("Message = %q, want the retried summary", resp.Message)
	}
	gateway := runner.modelGateway.(*sequenceGateway)
	if gateway.next != 3 {
		t.Fatalf("model called %d times, want 3 (the nudge costs one turn)", gateway.next)
	}
	last := gateway.continueRequests[len(gateway.continueRequests)-1].Messages
	nudge := last[len(last)-1]
	if nudge.Role != "user" {
		t.Fatalf("nudge role = %q, want user", nudge.Role)
	}
	if strings.TrimSpace(strings.TrimPrefix(nudge.Content, internalNudgePrefix)) == "" {
		t.Fatalf("nudge reaches the model as an empty user turn: %q", nudge.Content)
	}
	for _, want := range []string{"had no answer for the task", "keep going with tool calls", "final summary"} {
		if !strings.Contains(nudge.Content, want) {
			t.Fatalf("nudge = %q, want it to contain %q", nudge.Content, want)
		}
	}
	if len(*notices) != 1 || (*notices)[0] != "nudging for a task summary" {
		t.Fatalf("notices = %v, want one summary notice", *notices)
	}
}

// An answer that names the work is left alone, however chatty its wording.
func TestRunnerKeepsFinalAnswerNamingTheWork(t *testing.T) {
	runner := testRunner(t, okHandler, []string{
		demoCallWithPath,
		`{"type":"reply","message":"Hey! I read notes/todo.md for you."}`,
	})
	notices := noticeTexts(t, runner)

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, "I read notes/todo.md") {
		t.Fatalf("Message = %q, want the first answer kept", resp.Message)
	}
	if len(*notices) != 0 {
		t.Fatalf("notices = %v, want none", *notices)
	}
}

// The nudge is spent once per request: a second task-free answer is final.
func TestRunnerNudgesForASummaryOnlyOnce(t *testing.T) {
	runner := testRunner(t, okHandler, []string{
		demoCallWithPath,
		`{"type":"reply","message":"Understood."}`,
		`{"type":"reply","message":"I am your friendly assistant."}`,
	})
	notices := noticeTexts(t, runner)

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != "I am your friendly assistant." {
		t.Fatalf("Message = %q, want the second answer as final", resp.Message)
	}
	if len(*notices) != 1 {
		t.Fatalf("notices = %v, want exactly one", *notices)
	}
}

// taskFree recognises offer-to-help / ask-for-task phrasing as a dead-end
// answer, but leaves real (if terse) answers alone.
func TestTaskFreeMarkers(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		want   bool
	}{
		{
			name:   "offer to help after a correct run",
			answer: "I'd be happy to help with that! Could you tell me more about what you're working on?",
			want:   true,
		},
		{name: "done is a real answer", answer: "done", want: false},
		{name: "stopped is a real answer", answer: "stopped", want: false},
		{name: "specific summary is a real answer", answer: "patched and the tests pass", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := taskFree(c.answer); got != c.want {
				t.Fatalf("taskFree(%q) = %v, want %v", c.answer, got, c.want)
			}
		})
	}
}

// A turn that made tool calls is still mid-work and is never nudged.
func TestRunnerDoesNotNudgeMidWorkTurn(t *testing.T) {
	if got := (&Runner{}).nudgeSummary(context.Background(), RunRequest{SessionID: "s1"}, &runState{callCount: 1}, turnResult{answer: "Understood.", toolCallCount: 1}); got {
		t.Fatal("nudged a turn that made tool calls")
	}
}

// A thread that ran no tool is never nudged: there is no work to summarise.
func TestRunnerDoesNotNudgeToolFreeThread(t *testing.T) {
	runner := testRunner(t, okHandler, []string{`{"type":"reply","message":"Hey! What is up?"}`})
	notices := noticeTexts(t, runner)

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "hi"}); err != nil {
		t.Fatal(err)
	}
	if len(*notices) != 0 {
		t.Fatalf("notices = %v, want none", *notices)
	}
}
