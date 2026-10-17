package clients

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
)

// A stream that ends without a chunk is a well-formed exchange that carried no
// generation. The router reports it as KindEmptyResponse, so invokr classifies
// it by kind rather than by matching the router's message text.
func TestClient_EmptyStreamIsClassifiedNotTextMatched(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClientWithSettings(server.URL, "", 0, 2)
	_, err := client.Completion(context.Background(), inference.Request{
		Model:    testModel,
		Messages: []inference.Message{inference.UserMessage("hello")},
	})
	if err == nil {
		t.Fatal("expected an empty-response error, got nil")
	}
	if !inference.IsKind(err, inference.KindEmptyResponse) {
		t.Fatalf("error kind = %q, want empty_response: %v", inference.KindOf(err), err)
	}
	// The operator-facing wording still names the likely cause.
	if !strings.Contains(err.Error(), "without any completion chunks") {
		t.Fatalf("error did not explain the empty stream: %v", err)
	}
	// An empty generation is not retryable, so the turn is not re-sent.
	if got := calls.Load(); got != 1 {
		t.Fatalf("completion calls = %d, want 1", got)
	}
}

// A body the adapter cannot decode stays KindProtocol, so it remains
// distinguishable from an empty generation.
func TestClient_MalformedBodyStaysProtocolError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer server.Close()

	client := NewClientWithSettings(server.URL, "", 0, 0)
	_, err := client.CompletionOnce(context.Background(), inference.Request{
		Model:    testModel,
		Messages: []inference.Message{inference.UserMessage("hello")},
	})
	if err == nil {
		t.Fatal("expected a protocol error, got nil")
	}
	if !inference.IsKind(err, inference.KindProtocol) {
		t.Fatalf("error kind = %q, want protocol: %v", inference.KindOf(err), err)
	}
}

func TestIsContextLengthExceeded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"openai wording wrapped", fmt.Errorf("wrapped: %w", &inference.Error{Kind: inference.KindInvalidRequest, Status: 400, Message: "This model's maximum context length is 8192 tokens."}), true},
		{"openai code", &inference.Error{Kind: inference.KindInvalidRequest, Message: "context_length_exceeded"}, true},
		{"llama.cpp wording", &inference.Error{Kind: inference.KindInvalidRequest, Message: "the request exceeds the available context size"}, true},
		{"anthropic wording", &inference.Error{Kind: inference.KindInvalidRequest, Message: "prompt is too long: 210000 tokens > 200000 maximum"}, true},
		{"llama streamed overflow", &inference.Error{Kind: inference.KindUnavailable, Status: 500, Message: "server_error: Context size has been exceeded."}, true},
		{"llama request overflow", &inference.Error{Kind: inference.KindInvalidRequest, Status: 400, Message: "Context size has been exceeded."}, true},
		{"other unavailable", &inference.Error{Kind: inference.KindUnavailable, Status: 500, Message: "server_error: failed to load model"}, false},
		{"untyped llama wording", errors.New("Context size has been exceeded."), false},
		{"wrong kind llama wording", &inference.Error{Kind: inference.KindAuth, Message: "Context size has been exceeded."}, false},
		{"other invalid request", &inference.Error{Kind: inference.KindInvalidRequest, Message: "unknown field: foo"}, false},
		{"matching message but wrong kind", &inference.Error{Kind: inference.KindUnavailable, Message: "context_length_exceeded"}, false},
		{"untyped error mentioning context", errors.New("context length exceeded"), false},
	}
	for _, tc := range cases {
		if got := IsContextLengthExceeded(tc.err); got != tc.want {
			t.Fatalf("%s: IsContextLengthExceeded = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestClientStreamedContextOverflowIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"error\":{\"code\":500,\"message\":\"Context size has been exceeded.\",\"type\":\"server_error\"}}\n\n")
	}))
	defer server.Close()
	client := NewClientWithSettings(server.URL, "", 0, 0)
	defer func() { _ = client.Close() }()
	response, err := client.Completion(context.Background(), inference.Request{Model: testModel, Messages: []inference.Message{inference.UserMessage("hello")}})
	if response != nil || !inference.IsKind(err, inference.KindUnavailable) || !IsContextLengthExceeded(err) {
		t.Fatalf("want classified streamed overflow; response=%+v error=%v", response, err)
	}
}
