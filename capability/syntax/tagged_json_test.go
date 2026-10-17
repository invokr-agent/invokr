package syntax

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaggedJSONSyntaxParseReply(t *testing.T) {
	s := NewTaggedJSONSyntax()

	parsed, err := s.ParseAssistantOutput(`{"type":"reply","message":"hello"}`)
	if err != nil {
		t.Fatalf("ParseAssistantOutput returned error: %v", err)
	}

	if parsed.ToolCall != nil {
		t.Fatalf("expected no tool call, got %#v", parsed.ToolCall)
	}

	if parsed.Reply != "hello" {
		t.Fatalf("expected reply %q, got %q", "hello", parsed.Reply)
	}

	if parsed.Raw == "" {
		t.Fatal("expected raw output to be preserved")
	}
}

func TestTaggedJSONSyntaxParseToolCall(t *testing.T) {
	s := NewTaggedJSONSyntax()

	output := `<|tool_call>
{"name":"read_file","arguments":{"path":"main.go"}}
<|tool_call|>`

	parsed, err := s.ParseAssistantOutput(output)
	if err != nil {
		t.Fatalf("ParseAssistantOutput returned error: %v", err)
	}

	if parsed.ToolCall == nil {
		t.Fatal("expected tool call, got nil")
	}

	if parsed.ToolCall.Name != "read_file" {
		t.Fatalf("expected tool name %q, got %q", "read_file", parsed.ToolCall.Name)
	}

	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(parsed.ToolCall.Arguments, &args); err != nil {
		t.Fatalf("failed to unmarshal tool arguments: %v", err)
	}

	if args.Path != "main.go" {
		t.Fatalf("expected path %q, got %q", "main.go", args.Path)
	}

	if parsed.Reply != "" {
		t.Fatalf("expected empty reply for tool call, got %q", parsed.Reply)
	}
}

func TestTaggedJSONSyntaxParseToolCallWithSurroundingText(t *testing.T) {
	s := NewTaggedJSONSyntax()

	output := `I will inspect the file.
<|tool_call>
{"name":"read_file","arguments":{"path":"agent/session.go"}}
<|tool_call|>`

	parsed, err := s.ParseAssistantOutput(output)
	if err != nil {
		t.Fatalf("ParseAssistantOutput returned error: %v", err)
	}

	if parsed.ToolCall == nil {
		t.Fatal("expected tool call, got nil")
	}

	if parsed.ToolCall.Name != "read_file" {
		t.Fatalf("expected read_file, got %q", parsed.ToolCall.Name)
	}
}

func TestTaggedJSONSyntaxRejectsUnterminatedToolCall(t *testing.T) {
	s := NewTaggedJSONSyntax()

	_, err := s.ParseAssistantOutput(`<|tool_call>
{"name":"read_file","arguments":{"path":"main.go"}}`)

	if err == nil {
		t.Fatal("expected error for unterminated tool call")
	}
}

func TestTaggedJSONSyntaxRejectsInvalidToolJSON(t *testing.T) {
	s := NewTaggedJSONSyntax()

	_, err := s.ParseAssistantOutput(`<|tool_call>
{"name":
<|tool_call|>`)

	if err == nil {
		t.Fatal("expected error for invalid tool JSON")
	}
}

func TestTaggedJSONSyntaxRejectsMissingToolName(t *testing.T) {
	s := NewTaggedJSONSyntax()

	_, err := s.ParseAssistantOutput(`<|tool_call>
{"arguments":{"path":"main.go"}}
<|tool_call|>`)

	if err == nil {
		t.Fatal("expected error for missing tool name")
	}
}

func TestTaggedJSONSyntaxFallsBackToRawReply(t *testing.T) {
	s := NewTaggedJSONSyntax()

	parsed, err := s.ParseAssistantOutput(`plain text answer`)
	if err != nil {
		t.Fatalf("ParseAssistantOutput returned error: %v", err)
	}

	if parsed.ToolCall != nil {
		t.Fatalf("expected no tool call, got %#v", parsed.ToolCall)
	}

	if parsed.Reply != "plain text answer" {
		t.Fatalf("expected raw reply fallback, got %q", parsed.Reply)
	}
}

func TestTaggedJSONSyntaxParseReplyAfterChannelMarker(t *testing.T) {
	s := NewTaggedJSONSyntax()

	parsed, err := s.ParseAssistantOutput("<|channel>thought\n" + `{"type":"reply","message":"Hi! How can I help you today?"}`)
	if err != nil {
		t.Fatalf("ParseAssistantOutput returned error: %v", err)
	}

	if parsed.ToolCall != nil {
		t.Fatalf("expected no tool call, got %#v", parsed.ToolCall)
	}
	if parsed.Reply != "Hi! How can I help you today?" {
		t.Fatalf("expected clean reply, got %q", parsed.Reply)
	}
}

func TestTaggedJSONSyntaxRenderToolResult(t *testing.T) {
	s := NewTaggedJSONSyntax()

	msg := s.RenderToolResult(`file contents`)

	var rendered struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal tool result message: %v", err)
	}
	if err := json.Unmarshal(raw, &rendered); err != nil {
		t.Fatalf("failed to unmarshal tool result message: %v", err)
	}
	if rendered.Role != "user" {
		t.Fatalf("expected role %q, got %q", "user", rendered.Role)
	}

	var payload struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}

	if err := json.Unmarshal([]byte(rendered.Content), &payload); err != nil {
		t.Fatalf("tool result was not valid JSON: %v", err)
	}

	if payload.Type != "tool_result" {
		t.Fatalf("expected type %q, got %q", "tool_result", payload.Type)
	}

	if payload.Result != "file contents" {
		t.Fatalf("expected result %q, got %q", "file contents", payload.Result)
	}
}

func TestTaggedJSONSyntaxRenderResponseInstructionsTagged(t *testing.T) {
	s := NewTaggedJSONSyntax()

	prompt := s.RenderResponseInstructions(ResponseInstructionsInput{})

	required := []string{
		`{"type":"reply","message":"..."}`,
		"<|tool_call>",
		"<|tool_call|>",
		"Output exactly one valid response form.",
	}

	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected system prompt to contain %q\nprompt:\n%s", want, prompt)
		}
	}
}

func TestTaggedJSONSyntaxRenderResponseInstructionsNative(t *testing.T) {
	s := NewTaggedJSONSyntax()

	prompt := s.RenderResponseInstructions(ResponseInstructionsInput{
		NativeToolCalling: true,
	})

	for _, want := range []string{
		"Reply directly to the user.",
		"Use the provided native tools",
		"Never invent tool output.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected native instructions to contain %q\nprompt:\n%s", want, prompt)
		}
	}
}

func TestTaggedJSONSyntaxRenderToolExampleWrapsTaggedCall(t *testing.T) {
	s := NewTaggedJSONSyntax()

	got := s.RenderToolExample(`{"name":"read_file","arguments":{"path":"file.go"}}`)
	if !strings.Contains(got, "<|tool_call>") || !strings.Contains(got, "<|tool_call|>") {
		t.Fatalf("expected tagged wrapper, got %q", got)
	}
	if !strings.Contains(got, `"read_file"`) {
		t.Fatalf("expected example body, got %q", got)
	}
}
