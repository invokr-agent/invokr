package core

import "context"

type contextKey string

const sessionIDContextKey contextKey = "tools.session_id"
const delegateDepthContextKey contextKey = "tools.delegate_depth"

func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil || sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDContextKey, sessionID)
}

func SessionIDFromContext(ctx context.Context) string {
	return contextValue[string](ctx, sessionIDContextKey)
}

// contextValue reads key from a possibly-nil ctx, yielding T's zero value when absent.
func contextValue[T any](ctx context.Context, key contextKey) T {
	var value T
	if ctx != nil {
		value, _ = ctx.Value(key).(T)
	}
	return value
}

// WithDelegateDepth records how many levels of delegate_task nesting led to
// this context, so subagents can be prevented from delegating further.
func WithDelegateDepth(ctx context.Context, depth int) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, delegateDepthContextKey, depth)
}

func DelegateDepthFromContext(ctx context.Context) int {
	return contextValue[int](ctx, delegateDepthContextKey)
}

// ToolCallIDKey identifies the dispatch call whose tool emits progress.
type ToolCallIDKey struct{}

func ToolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ToolCallIDKey{}).(string)
	return id
}
