package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/support/testutil"
	"github.com/invokr-agent/invokr/tools/core"
)

func denyingHandler(executions *int) core.NativeHandler {
	return func(ctx context.Context, _ *core.ToolCall) (string, error) {
		*executions++
		err := core.EnforceApproval(ctx, testutil.StaticApprover{Decision: approval.Decision{Approved: false}}, core.ToolSpec{Name: "demo"}, core.Ask(approval.Request{Tool: "demo"}))
		return "", err
	}
}

func TestRunnerDoesNotRePromptForIdenticalDeniedCall(t *testing.T) {
	executions := 0
	runner := testRunner(t, denyingHandler(&executions), []string{
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`{"type":"reply","message":"done"}`,
	})

	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("handler executed %d times; the identical denied call must not re-prompt", executions)
	}
	if len(resp.ToolFailures) != 2 {
		t.Fatalf("ToolFailures = %#v, want two denied outcomes", resp.ToolFailures)
	}
	for _, outcome := range resp.ToolFailures {
		if outcome.Kind != "denied" {
			t.Fatalf("outcome kind = %q, want denied", outcome.Kind)
		}
	}
	if !strings.Contains(resp.ToolFailures[1].Detail, "not retried") {
		t.Fatalf("second outcome detail = %q, want a not-retried explanation", resp.ToolFailures[1].Detail)
	}
}

func TestRunnerAllowsDifferentCallAfterDeny(t *testing.T) {
	executions := 0
	runner := testRunner(t, denyingHandler(&executions), []string{
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`<|tool_call>{"name":"demo","arguments":{"a":2}}<|tool_call|>`,
		`{"type":"reply","message":"done"}`,
	})

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"}); err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("handler executed %d times; a different call after a deny must still run", executions)
	}
}

func TestRunnerReexecutesIdenticalSuccessfulCallInRequest(t *testing.T) {
	executions := 0
	runner := testRunner(t, func(context.Context, *core.ToolCall) (string, error) {
		executions++
		return "ok", nil
	}, []string{
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`{"type":"reply","message":"done"}`,
	})

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"}); err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("handler executed %d times; successful calls may observe changed state", executions)
	}
}

func TestRunnerResetsDeniedCallsBetweenRequests(t *testing.T) {
	executions := 0
	runner := testRunner(t, denyingHandler(&executions), []string{
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`{"type":"reply","message":"first"}`,
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`{"type":"reply","message":"second"}`,
	})
	runner.settings.MaxTurns = 2

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "two"}); err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("handler executed %d times; a fresh request must re-ask", executions)
	}
}

func TestRunnerResetsSuccessfulCallsBetweenRequests(t *testing.T) {
	executions := 0
	runner := testRunner(t, func(context.Context, *core.ToolCall) (string, error) {
		executions++
		return "ok", nil
	}, []string{
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`{"type":"reply","message":"first"}`,
		`<|tool_call>{"name":"demo","arguments":{"a":1}}<|tool_call|>`,
		`{"type":"reply","message":"second"}`,
	})
	runner.settings.MaxTurns = 2

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "two"}); err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("handler executed %d times; fresh request must run again", executions)
	}
}
