package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/invokr-agent/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
)

func (OpenAINativeToolCodec) NativeToolDefinitions(specs []core.ToolSpec) []inference.Tool {
	if len(specs) == 0 {
		return nil
	}
	defs := make([]inference.Tool, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) == "" {
			continue
		}
		// A deferred tool (schema not loaded) is listed in the prompt for
		// tool_search to fetch; shipping it here too with an open schema
		// invites a call that can only fail.
		if spec.Parameters == nil && !spec.Metadata.AlwaysVisible {
			continue
		}
		parameters := map[string]any{
			"type": "object", "additionalProperties": true,
		}
		if len(spec.Parameters) > 0 {
			parameters = spec.Parameters
		}
		defs = append(defs, inference.Tool{
			Name: spec.Name, Description: spec.NativeToolDescription(), Parameters: parameters,
		})
	}
	return defs
}

func (OpenAINativeToolCodec) NativeToolChoice(defs []inference.Tool) inference.ToolChoice {
	if len(defs) == 0 {
		return ""
	}
	return inference.ToolChoiceAuto
}

func (c OpenAINativeToolCodec) ToolCallFromNative(native inference.ToolCall) (*core.ToolCall, error) {
	name := strings.TrimSpace(native.Name)
	if name == "" {
		return nil, fmt.Errorf("native tool call missing function name")
	}

	args, err := c.rawToolArguments(native.Arguments)
	if err != nil {
		return nil, fmt.Errorf("native tool call %q has invalid arguments: %w", name, err)
	}

	return &core.ToolCall{Name: name, Arguments: args}, nil
}

// rawToolArguments normalizes the model's raw argument text. The contract types
// arguments as a JSON string owned by the host, so an empty or blank value is
// the model omitting arguments rather than an error.
func (OpenAINativeToolCodec) rawToolArguments(arguments string) (json.RawMessage, error) {
	if strings.TrimSpace(arguments) == "" {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid([]byte(arguments)) {
		return nil, fmt.Errorf("arguments string is not valid JSON (%d bytes, tail %q)",
			len(arguments), argumentsTail(arguments))
	}
	return json.RawMessage(arguments), nil
}

// argumentsTail returns the end of a rejected argument string. The tail is what
// distinguishes the failure shapes — a truncated string literal, a missing
// brace, a trailing comma — and a bounded excerpt keeps a file-sized argument
// out of the log.
func argumentsTail(arguments string) string {
	const maxTail = 120
	if len(arguments) <= maxTail {
		return arguments
	}
	return "..." + arguments[len(arguments)-maxTail:]
}

func (OpenAINativeToolCodec) AssistantMessage(content string, toolCalls []inference.ToolCall) inference.Message {
	if strings.TrimSpace(content) == "" {
		content = ""
	}
	return inference.AssistantMessage(content, toolCalls...)
}

func (OpenAINativeToolCodec) NativeToolResultMessage(toolCallID, result string) inference.Message {
	return inference.ToolMessage(strings.TrimSpace(toolCallID), result)
}
