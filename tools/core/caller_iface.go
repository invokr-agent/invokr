package core

import (
	"context"

	"github.com/invokr-ai/invokr/capability/approval"
)

// ToolCaller is the interface agent sessions use to get tool specs and execute
// calls. *tools.Caller satisfies this interface; so does filteredCaller.
type ToolCaller interface {
	PromptSpecs(sessionID string) []ToolSpec
	CallableSpecs(sessionID string) []ToolSpec
	Specs() []ToolSpec
	HandleCall(ctx context.Context, call *ToolCall) (string, error)
	HasNative(name string) bool
	NativeSource(name string) string
	MarkSchemasLoaded(sessionID string, names []string)
	// ForgetSession drops per-session state (schema-loaded tracking) when the
	// session ends, so long-lived callers shared across sessions do not
	// accumulate entries.
	ForgetSession(sessionID string)
	Approver() approval.Approver
	ReplaceNativeToolsBySource(source string, tools []DynamicNativeTool) []string
	RegisterNative(ToolSpec, NativeHandler)
}

// ApproverFrom returns the approver of a registrar that exposes one, or nil.
func ApproverFrom(c NativeRegistrar) approval.Approver {
	if r, ok := c.(interface{ Approver() approval.Approver }); ok {
		return r.Approver()
	}
	return nil
}
