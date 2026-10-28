package core

import (
	"context"

	"github.com/invokr-agent/invokr/capability/approval"
)

// ToolCaller is the interface agent sessions use to get tool specs and execute
// calls. *tools.Caller satisfies this interface; so does filteredCaller.
type ToolCaller interface {
	PromptSpecs(sessionID string) []ToolSpec
	StableSpecs() []ToolSpec
	Specs() []ToolSpec
	HandleCall(ctx context.Context, call *ToolCall) (string, error)
	HasNative(name string) bool
	NativeSource(name string) string
	// ForgetSession drops per-session state (enabled optional tools) when the
	// session ends, so long-lived callers shared across sessions do not
	// accumulate entries.
	ForgetSession(sessionID string)
	Approver() approval.Approver
	ReplaceNativeToolsBySource(source string, tools []DynamicNativeTool) []string
	RegisterNative(ToolSpec, NativeHandler)
}

func ApproverFrom(c NativeRegistrar) approval.Approver {
	if r, ok := c.(interface{ Approver() approval.Approver }); ok {
		return r.Approver()
	}
	return nil
}
