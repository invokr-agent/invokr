package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

func TestHTTPRejectsRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	t.Setenv("MCP_REDIRECT_TEST_KEY", "dummy-secret")
	client, err := newHTTPClient(config.MCPServerConfig{URL: source.URL, HeadersEnv: map[string]string{"X-API-Key": "MCP_REDIRECT_TEST_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = source.Client().Transport
	defer client.close()
	err = client.request(context.Background(), "notifications/initialized", nil, nil)
	if err == nil || reached.Load() {
		t.Fatalf("redirect must fail without reaching destination: err=%v reached=%v", err, reached.Load())
	}
}
