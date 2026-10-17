package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
	"github.com/stretchr/testify/require"
)

const testModel = "gpt-4o-mini"

func TestClient_Completion(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}

		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Model               string  `json:"model"`
			MaxCompletionTokens int     `json:"max_completion_tokens"`
			Temperature         float64 `json:"temperature"`
			Stream              bool    `json:"stream"`
			StreamOptions       struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
			ParallelToolCalls    bool `json:"parallel_tool_calls"`
			ChatCompletionTools  any  `json:"tools"`
			ChatCompletionChoice any  `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed decoding request: %v", err)
		}

		if req.Messages[0].Content != "hello" {
			t.Fatalf("expected prompt hello, got %#v", req.Messages[0].Content)
		}
		if req.MaxCompletionTokens != 64 {
			t.Fatalf("expected n_predict 64, got %d", req.MaxCompletionTokens)
		}
		if req.Temperature != 0.7 {
			t.Fatalf("expected temperature 0.7, got %v", req.Temperature)
		}
		if !req.Stream {
			t.Fatal("expected stream=true")
		}
		if !req.StreamOptions.IncludeUsage {
			t.Fatal("expected stream_options.include_usage=true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi there\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	resp, err := client.Completion(context.Background(), inference.Request{
		Model:           testModel,
		Messages:        []inference.Message{inference.UserMessage("hello")},
		MaxOutputTokens: 64,
		Temperature:     inference.Float(0.7),
	})
	if err != nil {
		t.Fatalf("Completion() returned error: %v", err)
	}

	require.NotNil(t, resp, "Completion() returned nil response")

	require.NotNil(t, resp, "Completion() returned nil response")
	if resp.Message.Content != "hi there" {
		t.Fatalf("expected content %q, got %q", "hi there", resp.Message.Content)
	}
	if resp.Usage.PromptTokens != 1 || resp.Usage.CompletionTokens != 2 || resp.Usage.TotalTokens != 3 {
		t.Fatalf("expected usage 1/2/3, got %#v", resp.Usage)
	}
}

func TestClient_MetadataReadsLlamaProps(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/props" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if model := r.URL.Query().Get("model"); model != "router model/1" {
			t.Fatalf("model query = %q, want router model/1", model)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret123" {
			t.Fatalf("expected Authorization header, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":131072}}`))
	}))
	defer server.Close()

	client := NewClientWithSettings(server.URL+"/v1", "secret123", 0, 0)
	metadata, err := client.Metadata(context.Background(), "router model/1")
	if err != nil {
		t.Fatalf("Metadata() returned error: %v", err)
	}
	if metadata.ContextWindowTokens != 131072 {
		t.Fatalf("expected context window 131072, got %d", metadata.ContextWindowTokens)
	}
	if metadata.Source != "llama.cpp /props" {
		t.Fatalf("expected metadata source, got %q", metadata.Source)
	}
}

func TestClient_MetadataMissingPropsIsUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client := NewClient(server.URL)
	metadata, err := client.Metadata(context.Background(), "test-model")
	if err != nil {
		t.Fatalf("Metadata() returned error: %v", err)
	}
	if metadata.ContextWindowTokens != 0 {
		t.Fatalf("expected no context window, got %d", metadata.ContextWindowTokens)
	}
}

func TestClient_StreamingCompletion(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"there\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	resp, err := client.Completion(context.Background(), inference.Request{
		Model:    testModel,
		Messages: []inference.Message{inference.UserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("Completion() returned error: %v", err)
	}
	if resp.Message.Content != "hi there" {
		t.Fatalf("expected content %q, got %q", "hi there", resp.Message.Content)
	}
}

func TestClient_CompletionOnce(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed decoding request: %v", err)
		}
		if req.Model != testModel {
			t.Fatalf("expected model %q, got %q", testModel, req.Model)
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "hello" {
			t.Fatalf("unexpected messages: %#v", req.Messages)
		}
		if req.Stream {
			t.Fatal("expected non-streaming request")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	resp, err := client.CompletionOnce(context.Background(), inference.Request{
		Model:    testModel,
		Messages: []inference.Message{inference.UserMessage("hello")},
	})
	if err != nil {
		t.Fatalf("CompletionOnce() returned error: %v", err)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("expected content %q, got %q", "hi", resp.Message.Content)
	}
}

func TestClient_SummarizeSendsModelAndPrompt(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed decoding request: %v", err)
		}
		if req.Model != "summary-model" {
			t.Fatalf("expected model %q, got %q", "summary-model", req.Model)
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "summarize this" {
			t.Fatalf("unexpected messages: %#v", req.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"summary-model","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	out, err := NewClient(server.URL).Summarize(context.Background(), SummaryInput{Prompt: "summarize this", Model: "summary-model"})
	if err != nil {
		t.Fatalf("Summarize() returned error: %v", err)
	}
	if out != "done" {
		t.Fatalf("expected %q, got %q", "done", out)
	}
}

func TestClient_Embeddings(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed decoding request: %v", err)
		}
		if req.Model != "memory-model" {
			t.Fatalf("expected model memory-model, got %q", req.Model)
		}
		if len(req.Input) != 2 || req.Input[0] != "one" || req.Input[1] != "two" {
			t.Fatalf("unexpected input: %#v", req.Input)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"memory-model","data":[{"object":"embedding","index":0,"embedding":[1,2]},{"object":"embedding","index":1,"embedding":[3,4]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	resp, err := client.Embeddings(context.Background(), inference.EmbeddingRequest{
		Model: "memory-model",
		Texts: []string{"one", "two"},
	})
	if err != nil {
		t.Fatalf("Embeddings() returned error: %v", err)
	}
	if len(resp) != 2 || resp[0][0] != 1 || resp[1][1] != 4 {
		t.Fatalf("unexpected embeddings: %#v", resp)
	}
}

func TestClient_APIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{
			"error": {
				"type": "bad_request",
				"message": "invalid prompt"
			}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	_, err := client.Completion(context.Background(), inference.Request{
		Model:    testModel,
		Messages: []inference.Message{inference.UserMessage("hello")},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "invalid prompt") {
		t.Fatalf("expected invalid prompt error, got %v", err)
	}
}

func TestClient_ToolCallParseErrorIsClassified(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{
			"error": {
				"type": "server_error",
				"message": "Failed to parse tool call arguments as JSON: unexpected end of input"
			}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	_, err := client.Completion(context.Background(), inference.Request{
		Model:    testModel,
		Messages: []inference.Message{inference.UserMessage("hello")},
	})
	if !inference.IsKind(err, inference.KindToolCallParse) {
		t.Fatalf("expected KindToolCallParse, got %v (kind %q)", err, inference.KindOf(err))
	}
	if !strings.Contains(err.Error(), "Failed to parse tool call arguments") {
		t.Fatalf("expected original message preserved, got %v", err)
	}
}

// TestClient_MetadataDetectsLlamaCppReasoningEffort covers the /props probe
// behind /reasoning: build_info marks llama-server and chat_template_caps
// says whether the template honours reasoning_effort.
func TestClient_MetadataDetectsLlamaCppReasoningEffort(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body   string
		kind   ReasoningKind
		effort bool
	}{
		"supported":   {`{"build_info":"b1","chat_template_caps":{"supports_reasoning_effort":true}}`, ReasoningLlamaCpp, true},
		"thinking":    {`{"build_info":"b1","chat_template_caps":{"supports_enable_thinking":true}}`, ReasoningLlamaCpp, false},
		"unsupported": {`{"build_info":"b1","chat_template_caps":{}}`, ReasoningLlamaCpp, false},
		"not llama":   {`{"max_model_len":4096}`, "", false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(tc.body))
		}))
		metadata, err := NewClient(server.URL+"/v1").Metadata(context.Background(), "m")
		server.Close()
		if err != nil {
			t.Fatalf("%s: Metadata() error: %v", name, err)
		}
		if metadata.Reasoning != tc.kind || metadata.ReasoningEffort != tc.effort {
			t.Fatalf("%s: Reasoning=%q ReasoningEffort=%v, want %q/%v", name, metadata.Reasoning, metadata.ReasoningEffort, tc.kind, tc.effort)
		}
	}
}

// TestClient_MetadataDetectsOtherReasoningScales covers the non-llama.cpp
// kinds: OpenRouter by host, Ollama by /api/version once /props is absent,
// and Anthropic from the router provider type without any request.
func TestClient_MetadataDetectsOtherReasoningScales(t *testing.T) {
	t.Parallel()

	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"version":"0.12.0"}`))
	}))
	defer ollama.Close()
	metadata, err := NewClient(ollama.URL+"/v1").Metadata(context.Background(), "m")
	if err != nil || metadata.Reasoning != ReasoningOllama {
		t.Fatalf("ollama: Reasoning=%q err=%v, want %q", metadata.Reasoning, err, ReasoningOllama)
	}

	client := NewClient("http://unused.invalid/v1")
	client.Router = RouterSpec{
		Providers: map[string]RouterProvider{"claude": {Type: "anthropic"}},
		Profiles:  map[string]RouterProfile{"claude": {Provider: "claude", Model: "m"}},
	}
	metadata, err = client.Metadata(context.Background(), "m")
	if err != nil || metadata.Reasoning != ReasoningAnthropic {
		t.Fatalf("anthropic: Reasoning=%q err=%v, want %q", metadata.Reasoning, err, ReasoningAnthropic)
	}

	// The host check needs no server: an unreachable openrouter.ai URL still
	// classifies, and the failed /props probe is reported, not swallowed.
	client = NewClient("https://openrouter.ai/api/v1")
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: r}, nil
	})}
	metadata, err = client.Metadata(context.Background(), "m")
	if err != nil || metadata.Reasoning != ReasoningOpenRouter {
		t.Fatalf("openrouter: Reasoning=%q err=%v, want %q", metadata.Reasoning, err, ReasoningOpenRouter)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
