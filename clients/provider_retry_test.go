package clients

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	inference "github.com/rivt-ai/go-inference-router"
)

func TestClient_RetriesRateLimitHonoringRetryAfter(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClientWithSettings(server.URL, "", 0, 2)
	started := time.Now()
	resp, err := client.CompletionOnce(context.Background(), inference.Request{
		Model:    testModel,
		Messages: []inference.Message{inference.UserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("CompletionOnce() returned error: %v", err)
	}
	if resp.Message.Content != "recovered" {
		t.Fatalf("content = %q, want recovered", resp.Message.Content)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("completion calls = %d, want 2", got)
	}
	if waited := time.Since(started); waited < time.Second {
		t.Fatalf("retried after %v, want at least the 1s Retry-After", waited)
	}
}

func TestClient_DoesNotRetryStalledStream(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done() // go silent, never sending a chunk
	}))
	defer server.Close()

	client := NewClientWithSettings(server.URL, "", 0, 2)
	client.SetStallTimeout(50 * time.Millisecond)

	_, err := client.Completion(context.Background(), inference.Request{
		Model:    testModel,
		Messages: []inference.Message{inference.UserMessage("hello")},
	})
	if err == nil {
		t.Fatal("expected a stall error, got nil")
	}
	if !inference.IsKind(err, inference.KindStalled) {
		t.Fatalf("error kind = %q, want stalled: %v", inference.KindOf(err), err)
	}
	// A silent provider already has the request in flight; re-sending it would
	// double the load rather than recover.
	if got := calls.Load(); got != 1 {
		t.Fatalf("completion calls = %d, want 1", got)
	}
}
