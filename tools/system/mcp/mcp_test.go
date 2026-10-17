package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/mcp"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMCPRegistersAndCallsStdioTool(t *testing.T) {
	t.Setenv("INVOKR_FAKE_MCP", "1")
	t.Cleanup(mcp.ShutdownClients)
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			MCP: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"fake": {Command: os.Args[0], Args: []string{"-test.run=TestFakeMCPServer"}, Enabled: true, DefaultToolsApprovalMode: constants.ApprovalPolicyAuto},
			}},
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Memory:  config.MemoryConfig{Dir: t.TempDir()},
		},
	})
	waitForMCPTool(t, caller, "mcp_fake_echo")
	if !slices.ContainsFunc(caller.Specs(), func(spec core.ToolSpec) bool { return spec.Name == "mcp_fake_echo" }) {
		t.Fatalf("expected imported MCP spec, got %+v", caller.Specs())
	}
	out, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "mcp_fake_echo", Arguments: json.RawMessage(`{"text":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "echo: hi" {
		t.Fatalf("unexpected MCP output: %q", out)
	}
	pidCall := &core.ToolCall{Name: "mcp_fake_echo", Arguments: json.RawMessage(`{"text":"__pid__"}`)}
	firstPID, err := caller.HandleCall(context.Background(), pidCall)
	if err != nil {
		t.Fatal(err)
	}
	secondPID, err := caller.HandleCall(context.Background(), pidCall)
	if err != nil {
		t.Fatal(err)
	}
	if firstPID != secondPID {
		t.Fatalf("MCP server restarted between calls: %q != %q", firstPID, secondPID)
	}
}

func TestMCPRequiresSchemaSearchPerSession(t *testing.T) {
	t.Setenv("INVOKR_FAKE_MCP", "1")
	t.Cleanup(mcp.ShutdownClients)
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			MCP: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"fake": {Command: os.Args[0], Args: []string{"-test.run=TestFakeMCPServer"}, Enabled: true, DefaultToolsApprovalMode: constants.ApprovalPolicyAuto},
			}},
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Memory:  config.MemoryConfig{Dir: t.TempDir()},
		},
	})
	waitForMCPTool(t, caller, "mcp_fake_echo")
	ctx := core.WithSessionID(context.Background(), "mcp-session")
	call := &core.ToolCall{Name: "mcp_fake_echo", Arguments: json.RawMessage(`{"text":"hi"}`)}
	if err := caller.StartToolSession("mcp-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.HandleCall(ctx, call); err == nil {
		t.Fatal("optional MCP call ran before enable")
	}
	if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "tools", Arguments: json.RawMessage(`{"action":"enable","names":["mcp_fake_echo"]}`)}); err != nil {
		t.Fatal(err)
	}
	managed, err := caller.ResolveToolCall(ctx, &core.ToolCall{Name: "tools", Arguments: json.RawMessage(`{"action":"run","name":"mcp_fake_echo","arguments":{"text":"hi"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := caller.HandleCall(ctx, managed); err != nil || out != "echo: hi" {
		t.Fatalf("managed MCP: %s %v", out, err)
	}
	if slices.ContainsFunc(caller.StableSpecs(), func(spec core.ToolSpec) bool { return spec.Name == "mcp_fake_echo" }) {
		t.Fatal("enable changed native array")
	}

}

func TestMCPApprovalRequiredByDefault(t *testing.T) {
	t.Setenv("INVOKR_FAKE_MCP", "1")
	t.Cleanup(mcp.ShutdownClients)
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			MCP: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"fake": {Command: os.Args[0], Args: []string{"-test.run=TestFakeMCPServer"}, Enabled: true},
			}},
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Memory:  config.MemoryConfig{Dir: t.TempDir()},
		},
		Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: false}},
	})
	waitForMCPTool(t, caller, "mcp_fake_echo")
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "mcp_fake_echo", Arguments: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "approval denied") {
		t.Fatalf("expected approval denial, got %v", err)
	}
}

func TestMCPSkipsNotificationBeforeResponse(t *testing.T) {
	t.Setenv("INVOKR_FAKE_MCP", "1")
	t.Cleanup(mcp.ShutdownClients)
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			MCP: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"fake": {Command: os.Args[0], Args: []string{"-test.run=TestFakeMCPServer"}, Enabled: true, DefaultToolsApprovalMode: constants.ApprovalPolicyAuto},
			}},
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Memory:  config.MemoryConfig{Dir: t.TempDir()},
		},
	})
	waitForMCPTool(t, caller, "mcp_fake_echo")
	out, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "mcp_fake_echo", Arguments: json.RawMessage(`{"text":"__notify__"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "echo: notified" {
		t.Fatalf("expected response to survive a preceding notification line, got %q", out)
	}
}

func TestFakeMCPServer(t *testing.T) {
	if os.Getenv("INVOKR_FAKE_MCP") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	initialized := false
	for {
		msg, err := readFakeMessage(reader)
		if err != nil {
			os.Exit(0)
		}
		var req fakeRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			os.Exit(1)
		}
		switch req.Method {
		case "initialize":
			if _, ok := req.Params.(map[string]any)["capabilities"]; !ok {
				os.Exit(2)
			}
			writeFakeResponse(req.ID, map[string]any{"protocolVersion": "2024-11-05"})
		case "notifications/initialized":
			var envelope map[string]any
			_ = json.Unmarshal(msg, &envelope)
			if _, exists := envelope["id"]; exists {
				os.Exit(3)
			}
			initialized = true
		case "tools/list":
			if !initialized {
				os.Exit(4)
			}
			writeFakeResponse(req.ID, map[string]any{"tools": []map[string]any{{"name": "echo", "description": "Echo text", "inputSchema": map[string]any{"type": "object"}}}})
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			data, _ := json.Marshal(req.Params)
			_ = json.Unmarshal(data, &params)
			text := fmt.Sprint(params.Arguments["text"])
			if text == "__pid__" {
				text = fmt.Sprint(os.Getpid())
			}
			if text == "__notify__" {
				writeFakeNotification()
				text = "notified"
			}
			writeFakeResponse(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "echo: " + text}}})
		}
	}
}

func waitForMCPTool(t *testing.T, caller *tools.Caller, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if slices.ContainsFunc(caller.Specs(), func(spec core.ToolSpec) bool { return spec.Name == name }) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP tool %q was not registered", name)
}

type fakeRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

func readFakeMessage(reader *bufio.Reader) ([]byte, error) {
	return reader.ReadBytes('\n')
}

func writeFakeResponse(id int, result any) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	_, _ = fmt.Fprintf(os.Stdout, "%s\n", payload)
}

// writeFakeNotification emits a JSON-RPC notification (no id field), which
// the protocol permits a server to send at any time, e.g. for logging.
func writeFakeNotification() {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{}})
	_, _ = fmt.Fprintf(os.Stdout, "%s\n", payload)
}
