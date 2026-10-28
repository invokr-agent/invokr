package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestShellCatRunsAsReadWithNoteAndWarn(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{Memory: config.MemoryConfig{Dir: t.TempDir()}}})
	defer caller.Close()
	var got string
	caller.RegisterNative(core.ToolSpec{Name: "read", Parameters: map[string]any{"type": "object"}, Metadata: core.ReadWorkspaceMetadata("system", core.ToolCapabilities{})},
		func(_ context.Context, call *core.ToolCall) (string, error) {
			got = string(call.Arguments)
			return "file text", nil
		})
	sink, logs := observer.New(zapcore.WarnLevel)
	p := &TurnProcessor{sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.New(sink)}

	out, _, outcome := p.executeTool(context.Background(), 1, "id", "native tool call", &core.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"cat a.txt"}`)})
	if outcome != nil || got != `{"paths":["a.txt"]}` {
		t.Fatalf("read args %q, outcome %+v", got, outcome)
	}
	if !strings.Contains(out, "file text") || !strings.Contains(out, "call read directly next time") {
		t.Fatalf("output %q, want read result plus note", out)
	}
	if logs.FilterMessage("native tool call rerouted from shell").Len() != 1 {
		t.Fatalf("warn logs: %+v", logs.All())
	}
}
