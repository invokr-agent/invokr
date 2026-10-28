package agent

import (
	"context"
	"github.com/invokr-agent/invokr/tools/core"
	"testing"
)

func TestToolDispatchProvidesCallID(t *testing.T) {
	var id string
	runner := testRunner(t, func(ctx context.Context, _ *core.ToolCall) (string, error) {
		id = core.ToolCallIDFromContext(ctx)
		return "ok", nil
	}, []string{`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`, `{"type":"reply","message":"done"}`})
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s", Message: "run"}); err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("tool received no call ID")
	}
}
