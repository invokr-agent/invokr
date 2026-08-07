package agent

import (
	"context"
	"strings"
	"testing"
)

// A run that answers a prompt naming a build command without touching a tool is
// nudged once to inspect the repository first (#442).
func TestRunnerNudgesZeroToolAnswerOnRepositoryTask(t *testing.T) {
	runner := testRunner(t, okHandler, []string{
		`{"type":"reply","message":"The tests almost certainly pass."}`,
		demoCallWithPath,
		`{"type":"reply","message":"I checked notes/todo.md and go test passes."}`,
	})
	notices := noticeTexts(t, runner)

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "does go test pass?"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, "notes/todo.md") {
		t.Fatalf("Message = %q, want the answer after the nudge", resp.Message)
	}
	gateway := runner.modelGateway.(*sequenceGateway)
	last := gateway.continueRequests[0].Messages
	nudge := last[len(last)-1]
	if nudge.Role != "user" || !strings.Contains(nudge.Content, "not run any tool yet") {
		t.Fatalf("nudge = %#v, want the inspect-first user message", nudge)
	}
	found := false
	for _, text := range *notices {
		if strings.Contains(text, "inspect the repository") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notices = %v, want the inspect-first notice", *notices)
	}
}

// The nudge fires at most once per run: a model that still answers tool-free is
// taken at its word rather than nudged again.
func TestRunnerNudgesZeroToolAnswerOnlyOnce(t *testing.T) {
	runner := testRunner(t, okHandler, []string{
		`{"type":"reply","message":"It passes."}`,
		`{"type":"reply","message":"It really passes."}`,
	})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "does go test pass?"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, "really passes") {
		t.Fatalf("Message = %q, want the second answer to be final", resp.Message)
	}
}

func TestNoToolNudgeStandsDown(t *testing.T) {
	cases := []struct {
		name    string
		message string
		answer  string
	}{
		{"clarification question", "does go test pass?", "Which package should I check?"},
		{"no path or command", "who wrote the go language?", "Google."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &runState{}
			req := RunRequest{SessionID: "s1", Message: tc.message}
			if (&Runner{}).nudgeNoTools(context.Background(), req, state, turnResult{answer: tc.answer}) {
				t.Fatalf("nudged on %q / %q", tc.message, tc.answer)
			}
		})
	}
}
