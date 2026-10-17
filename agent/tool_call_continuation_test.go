package agent

import (
	"context"
	"strings"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
)

func truncatedCallResponse(partial string) *inference.Response {
	return &inference.Response{
		FinishReason: inference.FinishLength,
		Message: inference.Message{
			Role:      inference.RoleAssistant,
			ToolCalls: []inference.ToolCall{{ID: "c1", Name: "edit", Arguments: partial}},
		},
		Usage: inference.Usage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 300},
	}
}

func TestTruncatedToolCallDetection(t *testing.T) {
	cases := []struct {
		name   string
		output completionOutput
		want   bool
	}{
		{"length cut mid-arguments", completionOutput{
			finishReason: inference.FinishLength,
			toolCalls:    []inference.ToolCall{{Name: "edit", Arguments: `{"path":"a.go","new":"func`}},
		}, true},
		{"length but arguments complete", completionOutput{
			finishReason: inference.FinishLength,
			toolCalls:    []inference.ToolCall{{Name: "edit", Arguments: `{"path":"a.go"}`}},
		}, false},
		{"invalid arguments but natural stop", completionOutput{
			finishReason: inference.FinishToolCalls,
			toolCalls:    []inference.ToolCall{{Name: "edit", Arguments: `{"path":`}},
		}, false},
		{"two calls", completionOutput{
			finishReason: inference.FinishLength,
			toolCalls:    []inference.ToolCall{{Arguments: `{`}, {Arguments: `{`}},
		}, false},
		{"empty arguments", completionOutput{
			finishReason: inference.FinishLength,
			toolCalls:    []inference.ToolCall{{Name: "edit", Arguments: "  "}},
		}, false},
	}
	for _, c := range cases {
		if got := truncatedToolCall(c.output); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestStitchArguments(t *testing.T) {
	cases := []struct {
		name, partial, continuation, want string
		ok                                bool
	}{
		{"raw concatenation", `{"path":"a.go","new":"func`, ` main() {}"}`, `{"path":"a.go","new":"func main() {}"}`, true},
		// A spurious leading newline is invalid raw (control char inside the
		// string literal), so the trimmed variant is what parses.
		{"fenced remainder", `{"path":"a.go","new":"x`, "```\n\"}\n```", `{"path":"a.go","new":"x"}`, true},
		{"full restatement", `{"path":"a.go","new":"x`, `{"path":"a.go","new":"xy"}`, `{"path":"a.go","new":"xy"}`, true},
		{"garbage", `{"path":"a.go`, `sorry, I cannot`, "", false},
		// The bench model supplies the rest of the string and forgets the final
		// closers; an in-string end is closed for it.
		{"missing closers", `{"path":"a.go","new":"func`, ` main() {\n\treturn strings.TrimSpace(os.Getenv(\"HOME\")) + suffix\n}\n`, `{"path":"a.go","new":"func main() {\n\treturn strings.TrimSpace(os.Getenv(\"HOME\")) + suffix\n}\n"}`, true},
		{"nested closers", `{"items":[{"task":"a`, `bcdefghijklmnopqrstuvwxyz abcdefghijklmnopqrstuvwxyz abcdefghijklmnopqrstuvwxyz`, `{"items":[{"task":"abcdefghijklmnopqrstuvwxyz abcdefghijklmnopqrstuvwxyz abcdefghijklmnopqrstuvwxyz"}]}`, true},
		{"short prose is not closed", `{"path":"a.go","new":"func`, `I am not able to continue.`, "", false},
		{"restated with prose", `{"path":"a.go","new":"x`, "Here it is:\n{\"path\":\"a.go\",\"new\":\"xy\"}\nDone.", `{"path":"a.go","new":"xy"}`, true},
		{"echoed prefix", `{"path":"a.go","new":"x`, `{"path":"a.go","new":"xy"}`, `{"path":"a.go","new":"xy"}`, true},
		{"unclosed bracket mismatch", `{"a":[1`, `,2}`, "", false},
		{"restatement shorter than partial is refused", `{"path":"a.go","old":"","new":"package main\n\nfunc main() {\n\tfmt.Println(1)\n`, `{"path":"a.go","old":"","new":""}`, "", false},
	}
	for _, c := range cases {
		got, ok := stitchArguments(c.partial, c.continuation)
		if ok != c.ok || got != c.want {
			t.Fatalf("%s: got %q,%v want %q,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// A completion cut mid-call is continued once: the follow-up request carries
// the partial arguments as an assistant prefix, runs without tool
// definitions, and its reply is stitched into a valid call.
func TestCompleteContinuesTruncatedToolCall(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		truncatedCallResponse(`{"path":"a.go","new":"func`),
		{
			FinishReason: inference.FinishStop,
			Message:      inference.Message{Role: inference.RoleAssistant, Content: ` main() {}"}`},
			Usage:        inference.Usage{PromptTokens: 150, CompletionTokens: 10, TotalTokens: 160},
		},
	}}
	gateway := testModelGateway(provider)

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("calls = %d, want the one continuation", provider.calls)
	}
	if len(output.toolCalls) != 1 || output.toolCalls[0].Arguments != `{"path":"a.go","new":"func main() {}"}` {
		t.Fatalf("toolCalls = %#v", output.toolCalls)
	}
	if output.finishReason != inference.FinishToolCalls {
		t.Fatalf("finishReason = %q", output.finishReason)
	}
	cont := provider.requests[1]
	if len(cont.Tools) != 0 {
		t.Fatalf("continuation request must carry no tool definitions, got %d", len(cont.Tools))
	}
	last := cont.Messages[len(cont.Messages)-1]
	if last.Role != inference.RoleAssistant || last.Content != "\n" {
		t.Fatalf("continuation suffix = %#v", last)
	}
	if !strings.Contains(cont.Messages[len(cont.Messages)-2].Content, "cut off by the output limit") {
		t.Fatalf("continuation prompt = %q", cont.Messages[len(cont.Messages)-2].Content)
	}
	prefix := cont.Messages[len(cont.Messages)-3]
	if prefix.Role != inference.RoleAssistant || prefix.Content != `{"path":"a.go","new":"func` {
		t.Fatalf("assistant prefix = %#v", prefix)
	}
	// Both generations charge the turn.
	if output.usage.TurnCompletionTokens != 210 || output.usage.TurnPromptTokens != 250 {
		t.Fatalf("usage = %+v", output.usage)
	}
}

// A continuation that does not stitch leaves the truncated call untouched for
// the normal rejection path, and only one attempt is made.
func TestCompleteContinuationFailureFallsThrough(t *testing.T) {
	partial := `{"path":"a.go","new":"func`
	provider := &scriptedProvider{outputs: []*inference.Response{
		truncatedCallResponse(partial),
		{Message: inference.Message{Role: inference.RoleAssistant, Content: "I am not able to continue."}},
	}}
	gateway := testModelGateway(provider)

	output, messages, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("calls = %d, want exactly one continuation attempt", provider.calls)
	}
	if len(output.toolCalls) != 1 || output.toolCalls[0].Arguments != partial {
		t.Fatalf("toolCalls = %#v, want the truncated call unchanged", output.toolCalls)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, continuation turns must stay request-local", len(messages))
	}
}
