package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeHTTPMCP answers initialize/tools/list/tools/call over SSE, prefixing
// every stream with a notification frame so the id filter is exercised.
func fakeHTTPMCP(t *testing.T, auth *string) *httptest.Server {
	t.Helper()
	var initialized atomic.Bool
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth != nil {
			*auth = r.Header.Get("Authorization")
		}
		var req fakeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			if _, ok := req.Params.(map[string]any)["capabilities"]; !ok {
				t.Error("missing capabilities")
			}
			result = map[string]any{"protocolVersion": "2024-11-05"}
		case "notifications/initialized":
			if req.ID != 0 || r.Header.Get("Mcp-Session-Id") != "session-1" {
				t.Error("invalid initialized notification/session")
			}
			initialized.Store(true)
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			if !initialized.Load() {
				t.Error("tools/list before initialized")
			}
			result = map[string]any{"tools": []map[string]any{{"name": "echo", "description": "Echo text", "inputSchema": map[string]any{"type": "object"}}}}
			if req.Params.(map[string]any)["cursor"] == nil {
				result = map[string]any{"tools": []any{}, "nextCursor": "echo-page"}
			} else if req.Params.(map[string]any)["cursor"] != "echo-page" {
				t.Error("discovery did not forward opaque cursor")
			}
		case "tools/call":
			var params struct {
				Arguments map[string]any `json:"arguments"`
			}
			data, _ := json.Marshal(req.Params)
			_ = json.Unmarshal(data, &params)
			result = map[string]any{"isError": params.Arguments["fail"] == true, "content": []map[string]any{{"type": "text", "text": "echo: " + fmt.Sprint(params.Arguments["text"])}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Mcp-Session-Id", "session-1")
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
	}))
}

func httpCaller(t *testing.T, server config.MCPServerConfig, approver approval.Approver) *tools.Caller {
	t.Helper()
	t.Cleanup(mcp.ShutdownClients)
	return tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			MCP:     config.MCPConfig{Servers: map[string]config.MCPServerConfig{"remote": server}},
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
		Approver: approver,
	})
}

func TestMCPHTTPRegistersAndCallsTool(t *testing.T) {
	var auth string
	srv := fakeHTTPMCP(t, &auth)
	defer srv.Close()
	t.Setenv("INVOKR_TEST_MCP_TOKEN", "secret-token")
	caller := httpCaller(t, config.MCPServerConfig{
		URL:                      srv.URL,
		HeadersEnv:               map[string]string{"Authorization": "INVOKR_TEST_MCP_TOKEN"},
		Enabled:                  true,
		DefaultToolsApprovalMode: constants.ApprovalPolicyAuto,
	}, nil)
	waitForMCPTool(t, caller, "mcp_remote_echo")
	out, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "mcp_remote_echo", Arguments: json.RawMessage(`{"text":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "echo: hi" {
		t.Fatalf("unexpected HTTP MCP output: %q", out)
	}
	if auth != "secret-token" {
		t.Fatalf("expected credential from HeadersEnv, got %q", auth)
	}
}

func TestMCPHTTPApprovalRequiredByDefault(t *testing.T) {
	srv := fakeHTTPMCP(t, nil)
	defer srv.Close()
	caller := httpCaller(t, config.MCPServerConfig{URL: srv.URL, Enabled: true},
		testutil.StaticApprover{Decision: approval.Decision{Approved: false}})
	waitForMCPTool(t, caller, "mcp_remote_echo")
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "mcp_remote_echo", Arguments: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "approval denied") {
		t.Fatalf("expected approval denial for remote MCP server, got %v", err)
	}
}

func TestMCPHTTPRejectsPlaintextRemote(t *testing.T) {
	caller := httpCaller(t, config.MCPServerConfig{URL: "http://mcp.example.com/rpc", Enabled: true, Required: true}, nil)
	waitForMCPTool(t, caller, "mcp_remote_error")
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "mcp_remote_error", Arguments: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("expected plaintext remote URL rejection, got %v", err)
	}
}

func TestMCPHTTPRejectsCommandAndURLTogether(t *testing.T) {
	caller := httpCaller(t, config.MCPServerConfig{URL: "https://mcp.example.com/rpc", Command: "echo", Enabled: true, Required: true}, nil)
	waitForMCPTool(t, caller, "mcp_remote_error")
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "mcp_remote_error", Arguments: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "both command and url") {
		t.Fatalf("expected command+url rejection, got %v", err)
	}
}

func TestMCPHTTPToolErrorPreservesFlaggedResult(t *testing.T) {
	srv := fakeHTTPMCP(t, nil)
	defer srv.Close()
	caller := httpCaller(t, config.MCPServerConfig{URL: srv.URL, Enabled: true, DefaultToolsApprovalMode: constants.ApprovalPolicyAuto}, nil)
	defer caller.Close()
	waitForMCPTool(t, caller, "mcp_remote_echo")
	out, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "mcp_remote_echo", Arguments: json.RawMessage(`{"text":"operation failed","fail":true}`)})
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != "mcp_tool_error" || toolErr.Payload != "echo: operation failed" || out != toolErr.Payload {
		t.Fatalf("result=%q error=%v; want flagged MCP failure with preserved text", out, err)
	}
}
