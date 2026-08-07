package core

import (
	"errors"
	"strings"
	"testing"
)

func TestRejectTemplateMarkupArgsRefusesALeakedDelimiter(t *testing.T) {
	call := &ToolCall{Name: "shell", Arguments: []byte(`{"command":"ls","cwd":".\n</function>\n</tool_call><tool_call>\n<function=read_file>"}`)}
	err := RejectTemplateMarkupArgs(call)
	if err == nil {
		t.Fatalf("expected a refusal for a cwd holding tool-call markup")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected a *ToolError, got %T", err)
	}
	if toolErr.Payload != "" {
		t.Fatalf("expected a hard failure (empty payload), got %q", toolErr.Payload)
	}
	if toolErr.Code != "malformed_call" {
		t.Fatalf("code = %q", toolErr.Code)
	}
	for _, want := range []string{"cwd", "resend"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q does not mention %q", err.Error(), want)
		}
	}
}

func TestRejectTemplateMarkupArgsLeavesGoodCallsAlone(t *testing.T) {
	for _, args := range []string{
		`{"command":"grep -rn \"</tool_call>\" .","cwd":"."}`,
		`{"path":"app/worker.py"}`,
		`{}`,
		`not json`,
	} {
		if err := RejectTemplateMarkupArgs(&ToolCall{Name: "shell", Arguments: []byte(args)}); err != nil {
			t.Fatalf("args %s refused: %v", args, err)
		}
	}
	if err := RejectTemplateMarkupArgs(nil); err != nil {
		t.Fatalf("nil call refused: %v", err)
	}
}
