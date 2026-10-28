package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/core"
)

func TestInitializationRejectsUnsupportedVersion(t *testing.T) {
	for _, version := range []string{"", "2099-01-01"} {
		t.Run(version, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req rpcRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Method != "initialize" {
					t.Errorf("unexpected method %s", req.Method)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": version}})
			}))
			defer server.Close()
			if _, err := startInitializedClient(context.Background(), config.MCPServerConfig{URL: server.URL}); err == nil || !strings.Contains(err.Error(), "unsupported MCP protocol version") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNotificationEncodingOmitsID(t *testing.T) {
	payload, err := encodeRPC(7, "notifications/initialized", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"jsonrpc":"2.0","method":"notifications/initialized"}` {
		t.Fatalf("notification = %s", payload)
	}
}

func TestReadHTTPMessageCorrelationAndEvents(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, want string
		bad                           bool
	}{
		{"JSON match", "application/json", `{"id":7,"result":{}}`, `{"id":7,"result":{}}`, false},
		{"JSON mismatch", "application/json", `{"id":8,"result":{}}`, "", true},
		{"JSON notification", "application/json", `{"method":"notifications/message"}`, "", true},
		{"multiline", "text/event-stream", "data: {\"id\":8,\"result\":{}}\r\n\r\n:comment\r\nevent: message\r\ndata: {\"id\":7,\r\ndata: \"result\":{}}\r\n\r\n", "{\"id\":7,\n\"result\":{}}\n", false},
		{"unterminated", "text/event-stream", "data: {\"id\":7}", "", true},
		{"oversized event", "text/event-stream", strings.Repeat("data: "+strings.Repeat("x", 1024)+"\n", 8192) + "\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{"Content-Type": []string{tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			got, err := readHTTPMessage(resp, 7)
			if (err != nil) != tc.bad || !tc.bad && string(got) != tc.want {
				t.Fatalf("message = %q, error=%v; want %q, bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestInitializationPropagatesNotificationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Method == "initialize" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
			return
		}
		if req.Method != "notifications/initialized" {
			t.Errorf("unexpected method %s", req.Method)
		}
		http.Error(w, "notification refused", http.StatusBadRequest)
	}))
	defer server.Close()
	if client, err := startInitializedClient(context.Background(), config.MCPServerConfig{URL: server.URL}); client != nil || err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("client=%v, error=%v", client, err)
	}
}

func TestStderrOnlyDecoratesErrors(t *testing.T) {
	client := &stdioClient{stderr: core.NewTruncatingWriters(64 * 1024)}
	_, _ = io.WriteString(client.stderr.Stderr(), "diagnostic")
	if err := client.withStderr(nil); err != nil {
		t.Fatalf("successful response became error: %v", err)
	}
	cause := errors.New("failure")
	if err := client.withStderr(cause); !errors.Is(err, cause) || !strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("lost error context: %v", err)
	}
}

func TestDecodeRPCRejectsMalformedEnvelopeFields(t *testing.T) {
	for _, payload := range []string{
		`{"id":"wrong","result":{"value":"accepted"}}`,
		`{"id":{},"result":{"value":"accepted"}}`,
		`{"id":7,"error":{"code":"wrong","message":"failure"}}`,
		`{"id":7,"error":{"code":{},"message":"failure"}}`,
	} {
		// Correlation deliberately forwards invalid envelopes to the decoder.
		if !messageMatchesID([]byte(payload), 7) {
			t.Fatalf("invalid envelope skipped: %s", payload)
		}
		var result map[string]any
		if err := decodeRPC("tools/list", []byte(payload), &result); err == nil || !strings.Contains(err.Error(), "parse MCP response") {
			t.Fatalf("payload %s: error=%v", payload, err)
		}
	}
}

func TestSafeNamePreservesNormalization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {" --__ ", ""}, {"  Server...Tool__42  ", "server_tool_42"},
		{"ÉCHO/工具/test", "cho_test"}, {"A\xffB", "a_b"}, {"Kelvin", "kelvin"},
	} {
		if got := safeName(tc.input); got != tc.want {
			t.Errorf("safeName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
