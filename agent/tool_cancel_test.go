package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"

	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools/core"
)

// TestCancelledToolEmitsTerminalEvent guards issue #300: when a tool call is
// interrupted the runner must still emit a terminal tool event, or the TUI's
// block stays "running" with a ticking timer forever.
func TestCancelledToolEmitsTerminalEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := testRunner(t, func(context.Context, *core.ToolCall) (string, error) {
		cancel() // the user pressed ctrl+c mid-execution
		return "", context.Canceled
	}, []string{
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`{"type":"reply","message":"stopped"}`,
	})

	var terminal []events.Event
	unregister := runner.events.(*events.OrderedDispatcher).Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventToolFailed || ev.Type == events.EventToolCompleted {
			terminal = append(terminal, ev)
		}
		return nil
	}))
	defer unregister()

	resp, err := runner.Run(ctx, RunRequest{SessionID: "s1", Message: "run"})

	if len(terminal) != 1 || terminal[0].Tool == nil {
		t.Fatalf("want one terminal tool event, got %#v", terminal)
	}
	if got := terminal[0].Tool.Code; got != ToolCodeCanceled {
		t.Fatalf("terminal event code = %q, want %q", got, ToolCodeCanceled)
	}
	if err == nil && len(resp.ToolFailures) > 0 && resp.ToolFailures[0].Kind != turn.OutcomeKindCanceled {
		t.Fatalf("ledger outcome = %#v, want canceled", resp.ToolFailures)
	}
}

// TestCancelWinsOverToolResult covers the shapes a tool can return while the
// turn is being interrupted (issue #300 follow-up): a backend that returns a
// successful result after its child was killed (code_interpreter's process
// backend), one that reports its own exit code (shell), and an approval that
// comes back denied because the queue was cleared by the same ctrl+c. All must
// settle as canceled, not completed/denied.
func TestCancelWinsOverToolResult(t *testing.T) {
	tests := []struct {
		name   string
		result func() (string, error)
	}{
		{name: "success after cancel", result: func() (string, error) { return "partial output after kill", nil }},
		{name: "tool code after cancel", result: func() (string, error) {
			return "", &core.ToolError{Code: "exit -1", Err: errors.New("signal: killed")}
		}},
		{name: "flagged payload after cancel", result: func() (string, error) {
			return "", &core.ToolError{Code: "exit -1", Err: errors.New("signal: killed"), Payload: "75 bytes of output"}
		}},
		{name: "denied during cancel", result: func() (string, error) {
			return "", &approval.DeniedError{Reason: "interrupted by user"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := testRunner(t, func(context.Context, *core.ToolCall) (string, error) {
				cancel()
				return tt.result()
			}, []string{
				`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
				`{"type":"reply","message":"stopped"}`,
			})

			var terminal []events.Event
			unregister := runner.events.(*events.OrderedDispatcher).Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
				if ev.Type == events.EventToolFailed || ev.Type == events.EventToolCompleted {
					terminal = append(terminal, ev)
				}
				return nil
			}))
			defer unregister()

			resp, err := runner.Run(ctx, RunRequest{SessionID: "s1", Message: "run"})

			if len(terminal) != 1 || terminal[0].Tool == nil {
				t.Fatalf("want one terminal tool event, got %#v", terminal)
			}
			if terminal[0].Type != events.EventToolFailed {
				t.Fatalf("terminal event type = %s, want tool failed", terminal[0].Type)
			}
			if got := terminal[0].Tool.Code; got != ToolCodeCanceled {
				t.Fatalf("terminal event code = %q, want %q", got, ToolCodeCanceled)
			}
			if err == nil && len(resp.ToolFailures) > 0 {
				outcome := resp.ToolFailures[0]
				if outcome.Kind != turn.OutcomeKindCanceled || !strings.Contains(outcome.Detail, "interrupted by user") {
					t.Fatalf("ledger outcome = %#v, want canceled with an informative reason", outcome)
				}
			}
		})
	}
}
