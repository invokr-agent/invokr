package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

func TestCompletionRequestsKeepSessionTools(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		{Message: inference.Message{Content: "ok"}},
		{Message: inference.Message{Content: "ok"}},
		{Message: inference.Message{Content: "summary"}},
		{Message: inference.Message{Content: "ok"}},
		{Message: inference.Message{Content: "1}"}},
	}}
	caller := &tools.Caller{}
	var deferredParams map[string]any
	for _, name := range []string{"read", "edit"} {
		params := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "integer"}}}
		if name == "edit" {
			deferredParams = params
		}
		caller.RegisterNative(core.ToolSpec{Name: name, Parameters: params, Metadata: core.ToolMetadata{AlwaysVisible: name == "read"}},
			func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })
	}
	gateway := &OpenAIModelGateway{
		provider: provider, model: "test", settings: config.AgentConfig{NPredict: 4096},
		tools: caller, syntax: syntax.NewTaggedJSONSyntax(), codec: OpenAINativeToolCodec{}, logger: zap.NewNop(),
		providerContext: NewProviderContext(65536, "test"),
	}
	messages := []inference.Message{inference.UserMessage("work")}
	_, _ = gateway.PromptBreakdown("s1") // Metrics must not freeze before Begin.
	caller.RegisterNative(core.ToolSpec{Name: "shell", Parameters: map[string]any{"type": "object"}, Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })
	if _, err := gateway.completeAttempt(context.Background(), messages, "s1", false, false); err != nil {
		t.Fatal(err)
	}
	deferredParams["properties"].(map[string]any)["x"].(map[string]any)["type"] = "string"
	caller.RegisterNative(core.ToolSpec{Name: "late", Parameters: map[string]any{"type": "object"}, Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })
	if _, err := gateway.completeAttempt(context.Background(), messages, "s1", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.SummarizeConversation(context.Background(), messages); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.completeAttempt(context.Background(), messages, "s1", true, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := gateway.continueToolCall(context.Background(), messages, completionOutput{toolCalls: []inference.ToolCall{{Name: "read", Arguments: `{"x":`}}}); !ok {
		t.Fatal("continuation did not stitch")
	}
	if len(provider.requests) != 5 {
		t.Fatalf("requests = %d", len(provider.requests))
	}
	want, err := json.Marshal(provider.requests[0].Tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests[0].Tools) != 3 {
		t.Fatalf("first request tools = %s", want)
	}
	for i, req := range provider.requests {
		got, err := json.Marshal(req.Tools)
		if err != nil || string(got) != string(want) {
			t.Errorf("request %d tools = %s, want %s: %v", i, got, want, err)
		}
		if i >= 2 && req.ToolChoice != inference.ToolChoiceNone {
			t.Errorf("request %d tool choice = %q, want none", i, req.ToolChoice)
		}
	}
}

func TestDisableToolsDropsIgnoredProviderCall(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{{
		Message: inference.Message{Content: "done", ToolCalls: []inference.ToolCall{{Name: "read", Arguments: `{}`}}},
	}}}
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "read", Parameters: map[string]any{"type": "object"}, Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })
	gateway := &OpenAIModelGateway{
		provider: provider, model: "test", settings: config.AgentConfig{NPredict: 4096},
		tools: caller, syntax: syntax.NewTaggedJSONSyntax(), codec: OpenAINativeToolCodec{}, logger: zap.NewNop(),
		providerContext: NewProviderContext(65536, "test"),
	}
	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("finish")}, "s1", true, false, 0)
	if err != nil || output.content != "done" || len(output.toolCalls) != 0 {
		t.Fatalf("output = %+v, err = %v", output, err)
	}
	if provider.requests[0].ToolChoice != inference.ToolChoiceNone || len(provider.requests[0].Tools) != 1 {
		t.Fatalf("disabled request = %+v", provider.requests[0])
	}
}

func TestStableToolsSurvivePlanToAuto(t *testing.T) {
	policy := approval.NewPolicy(approval.Plan, false)
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{}, Policy: policy})
	defer caller.Close()
	caller.RegisterNative(core.ToolSpec{Name: "write_probe", Parameters: map[string]any{"type": "object"}, Metadata: core.ToolMetadata{Capabilities: core.ToolCapabilities{WritesWorkspace: true}}},
		func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })
	gateway := &OpenAIModelGateway{tools: caller, codec: OpenAINativeToolCodec{}, logger: zap.NewNop()}
	first, err := json.Marshal(gateway.stableToolDefs())
	if err != nil {
		t.Fatal(err)
	}
	if containsTool(gateway.stableToolDefs(), "write_probe") || !containsTool(gateway.stableToolDefs(), "edit") {
		t.Fatal("snapshot must contain protected edit but not optional write_probe")
	}
	if err := policy.SetMode(approval.Auto); err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(gateway.stableToolDefs())
	if err != nil || string(second) != string(first) {
		t.Fatalf("tools changed with approval mode: %v", err)
	}
}

func containsTool(defs []inference.Tool, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}
