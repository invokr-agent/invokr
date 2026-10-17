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

// Ordinary prose gets one nudge; copied tool-count metadata gets a separate retry.
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

func TestRunnerRecoversCopiedToolCountAfterZeroToolNudge(t *testing.T) {
	runner := testRunner(t, okHandler, []string{
		`{"type":"reply","message":"I'll inspect ui/status.txt first."}`,
		`{"type":"reply","message":"I'll inspect the current UI files first. [Tool calls this turn: 1]"}`,
		demoCallWithPath,
		`{"type":"reply","message":"I inspected notes/todo.md using the actual tool."}`,
	})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "Inspect ui/status.txt and report its contents."})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ToolCallCount != 1 || !strings.Contains(resp.Message, "actual tool") {
		t.Fatalf("copied count ended request without execution: %#v", resp)
	}
}

func TestCopiedToolCountRecoveryStandsDown(t *testing.T) {
	for _, tc := range []struct {
		name, answer  string
		nudges, calls int
	}{
		{"retry exhausted", "Inspecting. [Tool calls this turn: 1]", 2, 0},
		{"real calls", "Checked. [Tool calls this turn: 1]", 1, 1},
		{"zero count", "Blocked. [Tool calls this turn: 0]", 1, 0},
		{"inline example", "Example: `[Tool calls this turn: 1]`", 1, 0},
		{"quoted example", `Example: "[Tool calls this turn: 1]"`, 1, 0},
		{"fenced example", "Example:\n```\n[Tool calls this turn: 1]\n```", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &runState{noToolNudges: tc.nudges, callCount: tc.calls}
			if (&Runner{}).nudgeNoTools(context.Background(), RunRequest{Message: "Inspect ui/status.txt"}, state, turnResult{answer: tc.answer}) {
				t.Fatal("unexpected recovery nudge")
			}
		})
	}
}

func TestRunnerDoesNotRetryQuotedToolCount(t *testing.T) {
	for _, tc := range []struct{ name, second string }{
		{"blockquote", `{"type":"reply","message":"Example:\n> [Tool calls this turn: 1]"}`},
		{"explanation", `{"type":"reply","message":"The example footer is [Tool calls this turn: 1]"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := testRunner(t, okHandler, []string{
				`{"type":"reply","message":"I'll inspect ui/status.txt first."}`,
				tc.second,
				demoCallWithPath,
				`{"type":"reply","message":"Unexpected tool call."}`,
			})
			resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "Inspect ui/status.txt and report its contents."})
			if err != nil {
				t.Fatal(err)
			}
			if resp.ToolCallCount != 0 || !strings.Contains(resp.Message, "[Tool calls this turn: 1]") {
				t.Fatalf("quoted example triggered a second nudge: %#v", resp)
			}
			if got := len(runner.modelGateway.(*sequenceGateway).continueRequests); got != 1 {
				t.Fatalf("continuations=%d, want the ordinary single nudge", got)
			}
		})
	}
}

func TestCopiedToolCountRecoveryIsBounded(t *testing.T) {
	runner := testRunner(t, okHandler, []string{
		`{"type":"reply","message":"Inspecting. [Tool calls this turn: 1]"}`,
		`{"type":"reply","message":"Inspecting. [Tool calls this turn: 1]"}`,
		`{"type":"reply","message":"I cannot inspect this repository."}`,
	})
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "Inspect ui/status.txt"})
	if err != nil || resp.ToolCallCount != 0 || !strings.Contains(resp.Message, "cannot inspect") {
		t.Fatalf("response=%#v err=%v", resp, err)
	}
	if got := len(runner.modelGateway.(*sequenceGateway).continueRequests); got != 2 {
		t.Fatalf("continuations=%d, want 2", got)
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
