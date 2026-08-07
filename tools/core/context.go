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
	if ctx == nil {
		return ""
	}
	sessionID, _ := ctx.Value(sessionIDContextKey).(string)
	return sessionID
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
	if ctx == nil {
		return 0
	}
	depth, _ := ctx.Value(delegateDepthContextKey).(int)
	return depth
}
