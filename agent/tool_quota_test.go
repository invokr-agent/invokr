package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"

	"go.uber.org/zap"
)

func quotaProcessor(t *testing.T, quotas map[string]int) (*TurnProcessor, *int) {
	t.Helper()
	calls := 0
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "web_search", Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { calls++; return "results", nil })
	return &TurnProcessor{
		sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop(), quotas: quotas,
	}, &calls
}

func TestToolQuotaRefusesPastTheCapAndFlagsTheResult(t *testing.T) {
	processor, ran := quotaProcessor(t, map[string]int{"web_search": 2})
	call := &core.ToolCall{Name: "web_search", Arguments: []byte(`{"query":"go"}`)}
	for i := range 2 {
		output, _, outcome := processor.executeTool(context.Background(), i, "", "tool call", call)
		if outcome != nil || !strings.Contains(output, "results") {
			t.Fatalf("call %d = %q outcome %v, want the tool's own result", i, output, outcome)
		}
	}
	output, _, outcome := processor.executeTool(context.Background(), 2, "", "tool call", call)
	if *ran != 2 {
		t.Fatalf("tool ran %d times, want the quota of 2", *ran)
	}
	// Flagged, not failed: the model still sees the text, so it stops asking
	// instead of retrying a hard error.
	if outcome != nil {
		t.Fatalf("outcome = %#v, want a flagged completion", outcome)
	}
	if !strings.Contains(output, "web_search quota 2/2 used this run") {
		t.Fatalf("refusal = %q, want the quota message", output)
	}
}

func TestToolQuotaUnsetAndZeroAreUnlimited(t *testing.T) {
	for name, quotas := range map[string]map[string]int{
		"unnamed tool": {"delegate": 1},
		"zero quota":   {"web_search": 0},
	} {
		processor, ran := quotaProcessor(t, quotas)
		call := &core.ToolCall{Name: "web_search", Arguments: []byte(`{"query":"go"}`)}
		for i := range 4 {
			processor.executeTool(context.Background(), i, "", "tool call", call)
		}
		if *ran != 4 {
			t.Fatalf("%s: tool ran %d times, want 4", name, *ran)
		}
	}
}

// A new request starts with a fresh quota.
func TestToolQuotaResetsPerRun(t *testing.T) {
	processor, ran := quotaProcessor(t, map[string]int{"web_search": 1})
	call := &core.ToolCall{Name: "web_search", Arguments: []byte(`{"query":"go"}`)}
	processor.executeTool(context.Background(), 0, "", "tool call", call)
	processor.executeTool(context.Background(), 1, "", "tool call", call)
	processor.ResetCallHistory()
	processor.executeTool(context.Background(), 2, "", "tool call", call)
	if *ran != 2 {
		t.Fatalf("tool ran %d times, want one call per run", *ran)
	}
}
