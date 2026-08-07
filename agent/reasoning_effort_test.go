package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// TestGatewaySendsReasoningEffortPerProviderScale: the level rides the body
// in the active provider's own field, and is withheld when no scale is known
// or the level is off that scale.
func TestGatewaySendsReasoningEffortPerProviderScale(t *testing.T) {
	reply := &inference.Response{Message: inference.Message{Content: `{"type":"reply","message":"ok"}`}}
	for name, tc := range map[string]struct {
		meta  clients.ProviderMetadata
		level string
		key   string
		want  any
	}{
		"llama.cpp none thinking":    {clients.ProviderMetadata{Reasoning: clients.ReasoningLlamaCpp, EnableThinking: true}, "none", "chat_template_kwargs", "map[enable_thinking:false]"},
		"llama.cpp none effort only": {clients.ProviderMetadata{Reasoning: clients.ReasoningLlamaCpp, ReasoningEffort: true}, "none", "chat_template_kwargs", nil},
		"llama.cpp high thinking":    {clients.ProviderMetadata{Reasoning: clients.ReasoningLlamaCpp, EnableThinking: true}, "high", "chat_template_kwargs", nil},
		"llama.cpp":                  {clients.ProviderMetadata{Reasoning: clients.ReasoningLlamaCpp}, "high", "reasoning_effort", "high"},
		"openrouter":                 {clients.ProviderMetadata{Reasoning: clients.ReasoningOpenRouter}, "high", "reasoning", "map[effort:high]"},
		"anthropic":                  {clients.ProviderMetadata{Reasoning: clients.ReasoningAnthropic}, "high", "output_config", "map[effort:high]"},
		"ollama xhigh":               {clients.ProviderMetadata{Reasoning: clients.ReasoningOllama}, "xhigh", "reasoning_effort", nil},
		"unknown":                    {clients.ProviderMetadata{}, "high", "reasoning_effort", nil},
	} {
		provider := &scriptedProvider{outputs: []*inference.Response{reply}}
		pc := NewProviderContext(0, "test")
		pc.ApplyMetadata("test", tc.meta)
		gateway := &OpenAIModelGateway{
			provider: provider, model: "test", settings: config.AgentConfig{NativeToolRequestsDisabled: true},
			tools: &tools.Caller{}, syntax: syntax.NewTaggedJSONSyntax(), logger: zap.NewNop(),
			events: events.NewOrderedDispatcher(zap.NewNop()), providerContext: pc, reasoningEffort: tc.level,
		}
		if _, err := gateway.Continue(context.Background(), ContinueModelRequest{SessionID: "s1", Messages: []inference.Message{inference.UserMessage("hi")}}); err != nil {
			t.Fatalf("%s: continue: %v", name, err)
		}
		if level, ok := provider.requests[0].Extra["reasoning_effort"]; tc.meta.Reasoning == clients.ReasoningLlamaCpp && (!ok || level != tc.level) {
			t.Fatalf("%s: reasoning_effort = %v (present=%v), want %q", name, level, ok, tc.level)
		}
		got, ok := provider.requests[0].Extra[tc.key]
		if (tc.want == nil) == ok || (ok && fmt.Sprint(got) != tc.want) {
			t.Fatalf("%s: Extra[%s] = %v (present=%v), want %v", name, tc.key, got, ok, tc.want)
		}
	}
}

func TestGatewayBudgetsLlamaCppToolResults(t *testing.T) {
	for _, tc := range []struct {
		name         string
		kind         clients.ReasoningKind
		disableTools bool
		messages     []inference.Message
		wantBudget   int
	}{
		{"user default", clients.ReasoningLlamaCpp, false, []inference.Message{inference.UserMessage("hi")}, 0},
		{"normal result", clients.ReasoningLlamaCpp, false, []inference.Message{inference.ToolMessage("1", "BUILD OK")}, 1024},
		{"failed result", clients.ReasoningLlamaCpp, false, []inference.Message{inference.ToolMessage("1", `{"error":"edit failed"}`)}, 2048},
		{"large result", clients.ReasoningLlamaCpp, false, []inference.Message{inference.ToolMessage("1", strings.Repeat("x", 8192))}, 2048},
		{"batch failure", clients.ReasoningLlamaCpp, false, []inference.Message{inference.ToolMessage("1", "[exit code 1]"), inference.ToolMessage("2", "BUILD OK")}, 2048},
		{"no tools", clients.ReasoningLlamaCpp, true, []inference.Message{inference.ToolMessage("1", "BUILD OK")}, 0},
		{"other provider", clients.ReasoningOpenRouter, false, []inference.Message{inference.ToolMessage("1", "BUILD OK")}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedProvider{outputs: []*inference.Response{{Message: inference.Message{Content: `{"type":"reply","message":"ok"}`}}}}
			pc := NewProviderContext(0, "test")
			pc.ApplyMetadata("test", clients.ProviderMetadata{Reasoning: tc.kind})
			caller := &tools.Caller{}
			caller.RegisterNative(core.ToolSpec{Name: "demo", Parameters: map[string]any{"type": "object"}},
				func(context.Context, *core.ToolCall) (string, error) { return "ok", nil })
			caller.MarkSchemasLoaded("s1", []string{"demo"})
			gateway := &OpenAIModelGateway{
				provider: provider, model: "test", settings: config.AgentConfig{NPredict: 4096},
				tools: caller, syntax: syntax.NewTaggedJSONSyntax(), codec: OpenAINativeToolCodec{}, logger: zap.NewNop(),
				events: events.NewOrderedDispatcher(zap.NewNop()), providerContext: pc,
			}
			if _, err := gateway.Continue(context.Background(), ContinueModelRequest{SessionID: "s1", Messages: tc.messages, DisableTools: tc.disableTools}); err != nil {
				t.Fatal(err)
			}
			req := provider.requests[0]
			budget, ok := req.Extra["reasoning_budget_tokens"]
			if ok != (tc.wantBudget > 0) || (ok && budget != tc.wantBudget) {
				t.Fatalf("budget = %v (present=%v), want %d", budget, ok, tc.wantBudget)
			}
			if req.MaxOutputTokens != 4096 {
				t.Fatalf("max output = %d, want 4096", req.MaxOutputTokens)
			}
		})
	}
}

func TestWithThinkingDisabledZeroesReasoningEffort(t *testing.T) {
	out := withThinkingDisabled(map[string]any{"reasoning_effort": "high"})
	if out["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %v, want none", out["reasoning_effort"])
	}
	if _, ok := withThinkingDisabled(nil)["reasoning_effort"]; ok {
		t.Fatal("reasoning_effort added when it was never set")
	}
}
