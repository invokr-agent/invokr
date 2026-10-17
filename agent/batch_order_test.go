package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// A batch that writes or runs code executes in the model's order and stops at
// the first hard failure: the test after a rejected edit must not run against
// the unedited file and report a fix that never landed.
func TestNativeBatchWithWritesRunsInOrderAndSkipsAfterFailure(t *testing.T) {
	var order []string
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "edit", Metadata: core.ToolMetadata{AlwaysVisible: true, Capabilities: core.ToolCapabilities{WritesWorkspace: true}}}, func(context.Context, *core.ToolCall) (string, error) {
		order = append(order, "edit")
		return "", errors.New("old not found in file")
	})
	caller.RegisterNative(core.ToolSpec{Name: "shell", Metadata: core.ToolMetadata{AlwaysVisible: true, Capabilities: core.ToolCapabilities{ExecutesCode: true}}}, func(context.Context, *core.ToolCall) (string, error) {
		order = append(order, "shell")
		return "ok", nil
	})
	p := &TurnProcessor{events: events.NewOrderedDispatcher(zap.NewNop()), sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop()}
	calls := []inference.ToolCall{
		{ID: "1", Name: "edit", Arguments: `{"path":"a.py"}`},
		{ID: "2", Name: "shell", Arguments: `{"command":"pytest"}`},
	}
	if !p.batchNeedsOrder(calls) {
		t.Fatal("a batch with a plain edit must run in order")
	}
	res, msgs, err := p.nativeToolCall(context.Background(), 1, nil, "", completionOutput{toolCalls: calls})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "edit" {
		t.Fatalf("executed %v, want only edit", order)
	}
	if res.toolCallCount != 1 || len(res.outcomes) != 1 {
		t.Fatalf("result = %+v, want one counted call and one outcome", res)
	}
	last := msgs[len(msgs)-1].Content
	if !strings.Contains(last, "skipped: an earlier tool call in this batch failed (edit: old not found in file)") || !strings.Contains(last, "call shell again") {
		t.Fatalf("skipped stub = %q", last)
	}
}

func TestNativeBatchWithoutPlainEditStaysParallel(t *testing.T) {
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "read_file", Metadata: core.ToolMetadata{AlwaysVisible: true, Capabilities: core.ToolCapabilities{ReadsWorkspace: true}}}, func(context.Context, *core.ToolCall) (string, error) {
		return "ok", nil
	})
	caller.RegisterNative(core.ToolSpec{Name: "shell", Metadata: core.ToolMetadata{AlwaysVisible: true, Capabilities: core.ToolCapabilities{WritesWorkspace: true, ExecutesCode: true}}}, func(context.Context, *core.ToolCall) (string, error) {
		return "ok", nil
	})
	p := &TurnProcessor{tools: caller}
	if p.batchNeedsOrder([]inference.ToolCall{{ID: "1", Name: "read_file"}, {ID: "2", Name: "read_file"}}) {
		t.Fatal("two read_file calls are a read-only batch")
	}
	// Two commands are independent: serializing them would also serialize
	// their approvals, which the batch approval queue exists to avoid.
	if p.batchNeedsOrder([]inference.ToolCall{{ID: "1", Name: "shell"}, {ID: "2", Name: "shell"}}) {
		t.Fatal("two shell calls must stay parallel")
	}
	if p.batchNeedsOrder([]inference.ToolCall{{ID: "1", Name: "read_file"}, {ID: "2", Name: "unknown_tool"}}) {
		t.Fatal("an unknown tool fails on its own; it must not serialize the batch")
	}
}
