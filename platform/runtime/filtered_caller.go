package runtime

import (
	"context"
	"fmt"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/tools/core"
)

// filteredCaller wraps a ToolCaller and restricts it to an allowlist of tool
// names. If allowlist is empty (non-nil), no tools are visible or callable.
type filteredCaller struct {
	inner   core.ToolCaller
	allowed map[string]struct{}
}

func newFilteredCaller(inner core.ToolCaller, allowlist []string) *filteredCaller {
	m := make(map[string]struct{}, len(allowlist))
	for _, name := range allowlist {
		m[name] = struct{}{}
	}
	return &filteredCaller{inner: inner, allowed: m}
}

func (f *filteredCaller) filterSpecs(specs []core.ToolSpec) []core.ToolSpec {
	out := make([]core.ToolSpec, 0, len(specs))
	for _, s := range specs {
		if _, ok := f.allowed[s.Name]; ok {
			out = append(out, s)
		}
	}
	return out
}

func (f *filteredCaller) Specs() []core.ToolSpec { return f.filterSpecs(f.inner.Specs()) }

func (f *filteredCaller) ModeInstructions() string {
	if c, ok := f.inner.(interface{ ModeInstructions() string }); ok {
		return c.ModeInstructions()
	}
	return ""
}

func (f *filteredCaller) Revision() uint64 {
	if c, ok := f.inner.(interface{ Revision() uint64 }); ok {
		return c.Revision()
	}
	return 0
}

func (f *filteredCaller) PromptSpecs(id string) []core.ToolSpec {
	return f.filterSpecs(f.inner.PromptSpecs(id))
}

func (f *filteredCaller) CallableSpecs(id string) []core.ToolSpec {
	return f.filterSpecs(f.inner.CallableSpecs(id))
}

func (f *filteredCaller) HandleCall(ctx context.Context, call *core.ToolCall) (string, error) {
	if _, ok := f.allowed[call.Name]; !ok {
		return "", &core.ToolError{
			Code: "not_allowed", Payload: fmt.Sprintf("tool %q is not available in this subagent", call.Name),
		}
	}
	return f.inner.HandleCall(ctx, call)
}

func (f *filteredCaller) HasNative(name string) bool {
	_, ok := f.allowed[name]
	return ok && f.inner.HasNative(name)
}

func (f *filteredCaller) NativeSource(name string) string { return f.inner.NativeSource(name) }

func (f *filteredCaller) MarkSchemasLoaded(id string, names []string) {
	f.inner.MarkSchemasLoaded(id, names)
}

func (f *filteredCaller) ForgetSession(id string) { f.inner.ForgetSession(id) }

func (f *filteredCaller) Approver() approval.Approver { return f.inner.Approver() }

// ReplaceNativeToolsBySource is a no-op on a filteredCaller: dynamic tool
// registration must not mutate the parent caller's global tool set on behalf
// of a restricted subagent, and newly registered tools would not be in this
// caller's allowlist anyway.
func (f *filteredCaller) ReplaceNativeToolsBySource(_ string, _ []core.DynamicNativeTool) []string {
	return nil
}

// RegisterNative is a no-op on a filteredCaller for the same reason as
// ReplaceNativeToolsBySource.
func (f *filteredCaller) RegisterNative(_ core.ToolSpec, _ core.NativeHandler) {}
