package core

import "context"

type ResultFilter interface {
	FilterToolResult(ctx context.Context, meta ToolResultMeta, input string) (string, error)
}

type ToolResultMeta struct {
	ToolName, Runtime, Source string
}

type NoopResultFilter struct{}

func (NoopResultFilter) FilterToolResult(_ context.Context, _ ToolResultMeta, input string) (string, error) {
	return input, nil
}
