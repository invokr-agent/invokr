package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

func TestManagedExecutionUsesTargetQuotaAndPolicy(t *testing.T) {
	policy := approval.NewPolicy(approval.Auto, false)
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{Memory: config.MemoryConfig{Dir: t.TempDir()}}, Policy: policy})
	defer caller.Close()
	ran := 0
	caller.RegisterNative(core.ToolSpec{Name: "probe", Parameters: map[string]any{"type": "object"}, Metadata: core.ReadWorkspaceMetadata("system", core.ToolCapabilities{})}, func(context.Context, *core.ToolCall) (string, error) { ran++; return "result", nil })
	if err := caller.StartToolSession("s1"); err != nil {
		t.Fatal(err)
	}
	p := &TurnProcessor{sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop(), quotas: map[string]int{"probe": 1}}
	invoke := func(args string) (string, string) {
		out, _, outcome := p.executeTool(context.Background(), 0, "id", "native tool call", &core.ToolCall{Name: "tools", Arguments: json.RawMessage(args)})
		if outcome != nil {
			return out, outcome.Tool
		}
		return out, ""
	}
	if _, target := invoke(`{"action":"enable","names":["probe"]}`); target != "" {
		t.Fatal(target)
	}
	wireCall := inference.ToolCall{ID: "wire-id", Name: "tools", Arguments: `{"action":"run","name":"probe","arguments":{}}`}
	res := p.processNativeCall(context.Background(), 1, wireCall)
	if res.outcome != nil || res.message.ToolCallID != "wire-id" || !strings.Contains(res.message.Content, "result") {
		t.Fatalf("wire correlation: %+v", res)
	}
	out, _ := invoke(wireCall.Arguments)
	if ran != 1 || !strings.Contains(out, "probe quota 1/1") || p.quotaUsed["probe"] != 1 {
		t.Fatalf("quota: ran=%d counts=%v out=%s", ran, p.quotaUsed, out)
	}
	caller.RegisterNative(core.ToolSpec{Name: "writer", Parameters: map[string]any{"type": "object"}, Metadata: core.ToolMetadata{Capabilities: core.ToolCapabilities{WritesWorkspace: true}}}, func(context.Context, *core.ToolCall) (string, error) { ran++; return "bad", nil })
	if _, target := invoke(`{"action":"enable","names":["writer"]}`); target != "" {
		t.Fatal(target)
	}
	if err := policy.SetMode(approval.Plan); err != nil {
		t.Fatal(err)
	}
	if _, target := invoke(`{"action":"run","name":"writer","arguments":{}}`); target != "writer" || ran != 1 {
		t.Fatalf("plan bypass, target=%q ran=%d", target, ran)
	}
}
func TestManagedToolsPreserveNativeBytesAndHistory(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{Memory: config.MemoryConfig{Dir: t.TempDir()}}})
	defer caller.Close()
	if err := caller.StartToolSession("s1"); err != nil {
		t.Fatal(err)
	}
	g := &OpenAIModelGateway{tools: caller, codec: OpenAINativeToolCodec{}, logger: zap.NewNop()}
	before, _ := json.Marshal(g.stableToolDefs())
	if !strings.Contains(string(before), `"name":"apply_patch"`) {
		t.Fatal("patch missing from native core")
	}
	ctx := core.WithSessionID(context.Background(), "s1")
	for _, args := range []string{`{"action":"list"}`, `{"action":"search","query":"code_interpreter"}`, `{"action":"enable","names":["code_interpreter"]}`, `{"action":"disable","names":["code_interpreter"]}`} {
		if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "tools", Arguments: json.RawMessage(args)}); err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(g.stableToolDefs())
		if string(before) != string(after) {
			t.Fatal("management changed native array")
		}
	}
	if len(g.stableToolDefs()) != 9 {
		t.Fatalf("native count: %d", len(g.stableToolDefs()))
	}
	p := &TurnProcessor{tools: caller}
	if !p.batchNeedsOrder([]inference.ToolCall{{Name: "tools"}, {Name: "tools"}}) {
		t.Fatal("enable/run batch must preserve order")
	}
}

func TestSessionToolDialogUsesManagedState(t *testing.T) {
	dir := t.TempDir()
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{Memory: config.MemoryConfig{Dir: dir}}})
	defer caller.Close()
	if err := caller.StartToolSession("dialog"); err != nil {
		t.Fatal(err)
	}
	session := &Session{sessionID: "dialog", tools: caller}
	list, err := session.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.Name == "read" && (!item.Protected || !item.Enabled) {
			t.Fatalf("core state = %+v", item)
		}
	}
	if err := session.SetToolEnabled(context.Background(), "read", false); err == nil {
		t.Fatal("protected tool disabled")
	}
	if err := session.SetToolEnabled(context.Background(), "code_interpreter", true); err != nil {
		t.Fatal(err)
	}
	restored := tools.NewCaller(core.CallerDeps{App: config.AppConfig{Memory: config.MemoryConfig{Dir: dir}}})
	defer restored.Close()
	if err := restored.StartToolSession("dialog"); err != nil {
		t.Fatal(err)
	}
	list, err = (&Session{sessionID: "dialog", tools: restored}).ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list {
		found = found || item.Name == "code_interpreter" && item.Enabled
	}
	if !found {
		t.Fatal("enabled tool did not persist")
	}
}

func TestManagedBatchUsesTargetCapabilities(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{Memory: config.MemoryConfig{Dir: t.TempDir()}}})
	defer caller.Close()
	p := &TurnProcessor{tools: caller}
	for _, tc := range []struct {
		args    string
		ordered bool
	}{
		{`{"action":"run","name":"apply_patch","arguments":{}}`, true},
		{`{"action":"run","name":"code_interpreter","arguments":{}}`, false},
		{`{"action":"enable","names":["code_interpreter"]}`, true},
	} {
		calls := []inference.ToolCall{{Name: "tools", Arguments: tc.args}, {Name: "shell", Arguments: `{}`}}
		if p.batchNeedsOrder(calls) != tc.ordered {
			t.Fatalf("ordering %s", tc.args)
		}
	}
}

func TestCompactionAppendsAuthoritativeToolState(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{Memory: config.MemoryConfig{Dir: t.TempDir()}}})
	defer caller.Close()
	if err := caller.StartToolSession("s"); err != nil {
		t.Fatal(err)
	}
	_, err := caller.HandleCall(core.WithSessionID(context.Background(), "s"), &core.ToolCall{Name: "tools", Arguments: json.RawMessage(`{"action":"enable","names":["code_interpreter"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	budget := &contextBudget{toolState: func() string { return caller.ToolStateReminder("s") }}
	messages := []inference.Message{inference.SystemMessage("fixed"), inference.UserMessage("summary")}
	for _, changed := range []bool{false, true} {
		out, _ := budget.afterCompaction(messages, contextWindowState{}, 100000, changed, 100000)
		if out[0].Content != "fixed" || out[1].Content != "summary" {
			t.Fatal("existing history changed")
		}
		if changed && (len(out) != 3 || !strings.Contains(out[2].Content, `["code_interpreter"]`)) {
			t.Fatalf("lost tool state: %+v", out)
		}
		if !changed && len(out) != 2 {
			t.Fatal("no-op compaction appended state")
		}
	}
}
