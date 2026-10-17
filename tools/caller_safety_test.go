package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
)

type emptyResultFilter struct{}

func (emptyResultFilter) FilterToolResult(context.Context, core.ToolResultMeta, string) (string, error) {
	return "", nil
}

func TestCallerHandleCallRecoversToolPanic(t *testing.T) {
	caller := NewCaller(core.CallerDeps{App: config.AppConfig{}})
	caller.RegisterNative(core.ToolSpec{Name: "panic"}, func(context.Context, *core.ToolCall) (string, error) {
		panic("boom")
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "panic"})
	if err == nil || err.Error() != "tool panicked: boom" {
		t.Fatalf("expected recovered panic error, got %v", err)
	}
}

func TestCallerHandleCallRejectsNilReceiver(t *testing.T) {
	var caller *Caller
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "demo"})
	if err == nil || err.Error() != "caller is nil" {
		t.Fatalf("error = %v, want caller is nil", err)
	}
}

func TestCallerHandleCallPreservesFlaggedResultWhenFilterRemovesOutput(t *testing.T) {
	caller := NewCaller(core.CallerDeps{App: config.AppConfig{}})
	caller.filter = emptyResultFilter{}
	caller.RegisterNative(core.ToolSpec{Name: "flagged"}, func(context.Context, *core.ToolCall) (string, error) {
		return "", &core.ToolError{Code: "flagged", Payload: "sensitive"}
	})

	got, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "flagged"})
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected ToolError, got %v", err)
	}
	if got != "[output removed by filter]" || toolErr.Payload != got {
		t.Fatalf("expected filtered placeholder, got output %q payload %q", got, toolErr.Payload)
	}
}
