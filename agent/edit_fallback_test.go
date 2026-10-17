package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	turnpkg "github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools/core"
	"go.uber.org/zap"
)

func TestEditFailurePreservesToolGuidanceAndRepeatIdentity(t *testing.T) {
	p := &TurnProcessor{logger: zap.NewNop()}
	call := &core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"normalized/verify.py","edits":[{"oldText":"import csv","newText":"import csv\nimport sys"}]}`)}
	var guard repeatGuard
	for i := 1; i <= maxIdenticalToolCallRepeats; i++ {
		payload, hash, outcome := p.handleToolError(context.Background(), i, "call", "native tool call", call, 0, errors.New("file does not exist; use content to create it"))
		if !strings.Contains(payload, "use content to create it") || strings.Contains(payload, "failures in a row") {
			t.Fatalf("tool guidance changed: %s", payload)
		}
		calls := []toolCallSignature{{Name: call.Name, Key: toolCallKey(call), Result: hash}}
		outcomes := []turnpkg.ToolOutcome{*outcome}
		nudged := guard.observe(turnSignatureKey(calls), failureSignatureKey(outcomes), failureToolKey(outcomes))
		if nudged != (i == maxIdenticalToolCallRepeats) {
			t.Fatalf("failure %d nudged=%v", i, nudged)
		}
	}
}
