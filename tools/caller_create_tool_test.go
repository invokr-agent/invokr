package tools

import (
	"context"
	"encoding/json"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools/core"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNewCaller_CreateToolMakesToolImmediatelyCallable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INVOKR_TOOLBOX_DIR", root)

	dispatcher := events.NewOrderedDispatcher(zap.NewNop())
	caller := NewCaller(
		core.CallerDeps{
			App:        testAppConfigFromEnv(t),
			Dispatcher: dispatcher,
			Approver:   testutil.StaticApprover{Decision: approval.Decision{Approved: true}},
		},
		zap.NewNop(),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	createArgs, err := json.Marshal(map[string]any{
		"name":        "word_count",
		"description": "Count words in text",
		"example":     `{"name":"word_count","arguments":{"text":"hi there"}}`,
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
			"required":   []string{"text"},
		},
		"python_source": "import json,sys\nargs=json.loads(sys.stdin.read())[\"arguments\"]\nprint(json.dumps({\"result\":str(len(args[\"text\"].split()))}), end=\"\")\n",
		"policy":        map[string]any{"tiers": []string{"executes_code"}, "approval": "ask"},
	})
	if err != nil {
		t.Fatalf("marshal create_tool args: %v", err)
	}

	if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "create_tool", Arguments: createArgs}); err != nil {
		t.Fatalf("create_tool call failed: %v", err)
	}

	if !caller.HasNative("word_count") {
		t.Fatal("expected newly created tool to be callable without restart")
	}

	output, err := caller.HandleCall(ctx, &core.ToolCall{
		Name:      "word_count",
		Arguments: json.RawMessage(`{"text":"hi there friend"}`),
	})
	if err != nil {
		t.Fatalf("invoke created tool failed: %v", err)
	}
	if output != "3" {
		t.Fatalf("expected created tool to report word count 3, got %q", output)
	}
}
