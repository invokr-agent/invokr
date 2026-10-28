package syntax

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/tools/core"
	inference "github.com/rivt-ai/go-inference-router"
)

const (
	taggedToolCallStart = "<|tool_call>"
	taggedToolCallEnd   = "<|tool_call|>"
)

type TaggedJSONSyntax struct{}

type taggedReply struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type taggedToolResult struct {
	Type   string `json:"type"`
	Result string `json:"result"`
}

func NewTaggedJSONSyntax() TaggedJSONSyntax { return TaggedJSONSyntax{} }

func (TaggedJSONSyntax) Name() string { return "tagged-json" }

func (TaggedJSONSyntax) RenderResponseInstructions(input ResponseInstructionsInput) string {
	if input.NativeToolCalling {
		return strings.TrimSpace(`

Reply directly to the user. Use the provided native tools when tool execution is needed.
- Never invent tool output.
- If a tool result reports an error or denial, do not claim the action succeeded; report the failure and why.
- Never write <|tool_call>, <|tool_response|>, <|thought|>, <|response|>, or hidden reasoning markers.
`)
	}

	return strings.TrimSpace(`

You have two valid response forms.

1. To reply to the user:
{"type":"reply","message":"..."}

2. To request tool execution:
<|tool_call>
{"name":"tool_name","arguments":{}}
<|tool_call|>

Rules:
- Output exactly one valid response form.
- If you output <|tool_call>, STOP immediately after <|tool_call|>.
- Never invent tool output.
- If a tool result reports an error or denial, do not claim the action succeeded; report the failure and why.
- Never write <|channel>thought, <|thought|>, <|response|>, or hidden reasoning markers.
- Do not explain tool calls.
- Do not include markdown.
`)
}

func (TaggedJSONSyntax) RenderToolExample(example string) string {
	example = strings.TrimSpace(example)
	if example == "" {
		return ""
	}
	return taggedToolCallStart + "\n" + example + "\n" + taggedToolCallEnd
}

func (TaggedJSONSyntax) ParseAssistantOutput(text string) (ParsedAssistantOutput, error) {
	raw := strings.TrimSpace(text)

	if call, ok, err := parseTaggedToolCall(raw); ok || err != nil {
		if err != nil {
			return ParsedAssistantOutput{Raw: raw}, err
		}
		return ParsedAssistantOutput{ToolCall: &call, Raw: raw}, nil
	}

	// The bare text first, then the first embedded JSON object: models often
	// wrap the reply form in prose or fences.
	for _, candidate := range []string{raw, strutil.ExtractJSONObject(raw)} {
		if reply, ok := parseTaggedReply(candidate); ok {
			return ParsedAssistantOutput{Reply: reply.Message, Raw: raw}, nil
		}
	}

	// Be forgiving: if the model did not produce the expected JSON reply form,
	// treat the raw content as the final reply instead of failing the whole turn.
	return ParsedAssistantOutput{Reply: raw, Raw: raw}, nil
}

func parseTaggedReply(raw string) (taggedReply, bool) {
	var reply taggedReply
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &reply); err != nil {
		return taggedReply{}, false
	}
	if reply.Type != "reply" {
		return taggedReply{}, false
	}
	return reply, true
}

func (TaggedJSONSyntax) RenderToolResult(result string) inference.Message {
	payload, _ := json.Marshal(taggedToolResult{Type: "tool_result", Result: result}) // Both fields are strings.

	return inference.UserMessage(string(payload))
}

func (TaggedJSONSyntax) StopSequences() []string {
	// Start empty. Add stop sequences once parser/backend behavior is confirmed.
	return nil
}

func parseTaggedToolCall(text string) (core.ToolCall, bool, error) {
	i := strings.Index(text, taggedToolCallStart)
	if i < 0 {
		return core.ToolCall{}, false, nil
	}

	bodyStart := i + len(taggedToolCallStart)
	j := strings.Index(text[bodyStart:], taggedToolCallEnd)
	if j < 0 {
		return core.ToolCall{}, true, fmt.Errorf("unterminated tool call")
	}
	var call core.ToolCall
	if err := json.Unmarshal([]byte(strings.TrimSpace(text[bodyStart:bodyStart+j])), &call); err != nil {
		return core.ToolCall{}, true, err
	}
	if strings.TrimSpace(call.Name) == "" {
		return core.ToolCall{}, true, fmt.Errorf("tool call missing name")
	}

	return call, true, nil
}
