package syntax

import (
	"github.com/invokr-ai/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
)

type ResponseInstructionsInput struct {
	NativeToolCalling bool
}

type ParsedAssistantOutput struct {
	// Reply is non-empty when the assistant produced a final user-facing answer.
	Reply string

	// ToolCall is non-nil when the assistant requested a tool.
	ToolCall *core.ToolCall

	// Raw is the original assistant text.
	Raw string
}

type PromptSyntax interface {
	// Name returns a stable identifier, for example "tagged-json".
	Name() string

	// RenderResponseInstructions returns syntax-specific response rules.
	RenderResponseInstructions(input ResponseInstructionsInput) string

	// RenderToolExample formats a tool-call example in this syntax.
	RenderToolExample(example string) string

	// ParseAssistantOutput parses raw model output into either a final reply
	// or a tool call.
	ParseAssistantOutput(text string) (ParsedAssistantOutput, error)

	// RenderToolResult converts raw tool output into the next message that
	// should be appended to the conversation.
	RenderToolResult(result string) inference.Message

	// StopSequences returns model stop strings suitable for this syntax.
	StopSequences() []string
}
