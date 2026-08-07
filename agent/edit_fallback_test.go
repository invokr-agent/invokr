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

func TestEditFallbackHintAfterCap(t *testing.T) {
	p := &TurnProcessor{}
	call := &core.ToolCall{Name: "edit"}
	err := errors.New("old not found in file")
	for i := 0; i < editRejectCap; i++ {
		if hint := p.editFallbackHint(call, err); hint != "" {
			t.Fatalf("hint before cap at failure %d: %q", i+1, hint)
		}
	}
	hint := p.editFallbackHint(call, err)
	if !strings.Contains(hint, "edit failures in a row") {
		t.Fatalf("expected fallback hint past cap, got %q", hint)
	}
	// A success resets the streak.
	p.callMu.Lock()
	p.editRejects = 0
	p.callMu.Unlock()
	if hint := p.editFallbackHint(call, err); hint != "" {
		t.Fatalf("hint after reset: %q", hint)
	}
}

func TestEditFallbackHintIgnoresOtherTools(t *testing.T) {
	p := &TurnProcessor{}
	err := errors.New("boom")
	for i := 0; i < editRejectCap+2; i++ {
		if hint := p.editFallbackHint(&core.ToolCall{Name: "shell"}, err); hint != "" {
			t.Fatalf("unexpected hint for shell: %q", hint)
		}
	}
}

// Replays the b63 M4 failure: recovery hints start on failure three, exactly
// when identical native errors must trip the existing repeat guard.
func TestEditFailureRepeatIdentitySurvivesHint(t *testing.T) {
	p := &TurnProcessor{logger: zap.NewNop()}
	call := &core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"normalized/verify.py","old":"import csv","new":"import csv\nimport sys"}`)}
	var guard repeatGuard
	for i := 1; i <= maxIdenticalToolCallRepeats; i++ {
		_, hash, outcome := p.handleToolError(context.Background(), i, "call", "native tool call", call, 0, errors.New("file does not exist; create it first with an empty old and the full contents in new"))
		calls := []toolCallSignature{{Name: call.Name, Key: toolCallKey(call), Result: hash}}
		outcomes := []turnpkg.ToolOutcome{*outcome}
		nudged := guard.observe(turnSignatureKey(calls), failureSignatureKey(outcomes), failureToolKey(outcomes))
		if nudged != (i == maxIdenticalToolCallRepeats) {
			t.Fatalf("failure %d nudged=%v", i, nudged)
		}
	}
}

func TestMissingFileKeepsCreationGuidance(t *testing.T) {
	p := &TurnProcessor{logger: zap.NewNop()}
	call := &core.ToolCall{Name: "edit"}
	err := &core.ToolError{Code: "missing_file", Err: errors.New("create it first with an empty old and the full contents in new")}
	for i := 0; i < 4; i++ {
		payload, _, _ := p.handleToolError(context.Background(), i, "call", "native tool call", call, 0, err)
		if strings.Contains(payload, "call read_file") || !strings.Contains(payload, "create it first") {
			t.Fatal(payload)
		}
	}
}
