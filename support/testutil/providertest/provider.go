package providertest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type Server struct {
	server *httptest.Server

	mu                 sync.Mutex
	steps              []CompletionStep
	completionIndex    int
	completionRequests []CompletionRequest
	embeddingRequests  []EmbeddingRequest
	errors             []string
	contextWindow      int
}

type Option func(*Server)

func WithContextWindow(tokens int) Option {
	return func(s *Server) { s.contextWindow = tokens }
}

type CompletionStep struct {
	Content           string
	ToolCalls         []CompletionToolCall
	SSEChunks         []CompletionStreamChunk
	Status            int
	Body              string
	RetryAfterSeconds int
	DropAfterChunks   int
	MalformedBody     string
	StallAfterChunks  int
	StallDuration     time.Duration
	BeforeResponse    func()
	MutateForRequest  func(CompletionRequest, *CompletionStep)
	AfterChunk        func(int)
	// Usage overrides the fixture's default usage object so a test can script
	// an OpenAI-style prompt_tokens_details.cached_tokens report.
	Usage *StepUsage
}

// StepUsage is the usage object one scripted step reports. CachedTokens is the
// cache-served subset of PromptTokens, exactly as OpenAI reports it.
type StepUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
}

func (u StepUsage) wire() map[string]any {
	body := map[string]any{
		"prompt_tokens":     u.PromptTokens,
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.PromptTokens + u.CompletionTokens,
	}
	if u.CachedTokens > 0 {
		body["prompt_tokens_details"] = map[string]any{"cached_tokens": u.CachedTokens}
	}
	return body
}

type CompletionToolCall struct {
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function CompletionToolFunction `json:"function"`
}

type CompletionToolFunction struct {
	Name string `json:"name,omitempty"`
	// Arguments is declared as any so fixtures can write a Go map, but the
	// OpenAI wire format carries it as a JSON-encoded string. MarshalJSON
	// does that encoding, so what a test declares reaches the client exactly
	// as a real provider would send it.
	Arguments any `json:"arguments,omitempty"`
}

func (f CompletionToolFunction) MarshalJSON() ([]byte, error) {
	wire := struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	}{Name: f.Name}
	switch value := f.Arguments.(type) {
	case nil:
	case string:
		wire.Arguments = value
	case json.RawMessage:
		wire.Arguments = string(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		wire.Arguments = string(encoded)
	}
	return json.Marshal(wire)
}

type CompletionStreamChunk struct {
	Content   string               `json:"content,omitempty"`
	ToolCalls []CompletionToolCall `json:"tool_calls,omitempty"`
	Stop      bool                 `json:"stop,omitempty"`
}

type ChatMessage struct {
	Role       string `json:"role"`
	Content    any    `json:"content"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolCalls  any    `json:"tool_calls,omitempty"`
}

type CompletionRequest struct {
	ReceivedAt          time.Time     `json:"-"`
	Messages            []ChatMessage `json:"messages"`
	Model               string        `json:"model,omitempty"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	Temperature         float64       `json:"temperature,omitempty"`
	Stream              bool          `json:"stream,omitempty"`
	Tools               any           `json:"tools,omitempty"`
	ToolChoice          any           `json:"tool_choice,omitempty"`
	ParallelToolCalls   bool          `json:"parallel_tool_calls,omitempty"`
}

type EmbeddingRequest struct {
	Input []string `json:"input"`
}

type Snapshot struct {
	CompletionRequests []CompletionRequest
	EmbeddingRequests  []EmbeddingRequest
	Errors             []string
}

func New(t testing.TB, steps []CompletionStep, opts ...Option) *Server {
	t.Helper()

	s := &Server{steps: steps}
	for _, opt := range opts {
		opt(s)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for provider fixture: %v", err)
	}
	s.server = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	s.server.Listener = listener
	s.server.Start()
	return s
}

func NewScripted(t testing.TB, steps ...CompletionStep) *Server {
	t.Helper()
	return New(t, steps)
}

func (s *Server) URL() string {
	return s.server.URL
}

func (s *Server) Close() {
	s.server.Close()
}

func (s *Server) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	return Snapshot{
		CompletionRequests: append([]CompletionRequest(nil), s.completionRequests...),
		EmbeddingRequests:  append([]EmbeddingRequest(nil), s.embeddingRequests...),
		Errors:             append([]string(nil), s.errors...),
	}
}

func (s *Server) AssertClean(t testing.TB) {
	t.Helper()
	if errors := s.Snapshot().Errors; len(errors) > 0 {
		t.Fatalf("provider errors: %v", errors)
	}
}

func (s *Server) AssertExhausted(t testing.TB) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completionIndex != len(s.steps) {
		t.Fatalf("provider consumed %d of %d completion steps", s.completionIndex, len(s.steps))
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/props":
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"default_generation_settings":{"n_ctx":%d}}`, s.contextWindow)
	case "/health":
		w.WriteHeader(http.StatusOK)
	case "/v1/chat/completions":
		s.handleCompletion(w, r)
	case "/v1/embeddings":
		s.handleEmbeddings(w, r)
	case "/v1/models":
		// Return 404 so resolveModel treats this provider as having no list endpoint.
		http.NotFound(w, r)
	default:
		s.recordError("unexpected provider path %s", r.URL.Path)
		http.NotFound(w, r)
	}
}

func (s *Server) handleCompletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.recordError("unexpected completion method %s", r.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.recordError("decode completion request: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.ReceivedAt = time.Now()

	step, ok := s.nextCompletionStep(req)
	if !ok {
		http.Error(w, "unexpected completion call", http.StatusInternalServerError)
		return
	}
	if step.BeforeResponse != nil {
		step.BeforeResponse()
	}
	s.writeCompletionStep(w, r, req, step)
}

func (s *Server) writeCompletionStep(w http.ResponseWriter, r *http.Request, req CompletionRequest, step CompletionStep) {
	if step.Status >= 400 {
		writeErrorStep(w, step)
		return
	}
	if step.MalformedBody != "" {
		writeMalformedStep(w, step.MalformedBody)
		return
	}
	if req.Stream || len(step.SSEChunks) > 0 {
		s.writeStreamStep(w, r, step)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(openAICompletionResponse(step))
}

func writeErrorStep(w http.ResponseWriter, step CompletionStep) {
	w.Header().Set("Content-Type", "application/json")
	if step.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(step.RetryAfterSeconds))
	}
	w.WriteHeader(step.Status)
	_, _ = w.Write([]byte(step.Body))
}

func writeMalformedStep(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	_, _ = w.Write([]byte(body))
}

func (s *Server) writeStreamStep(w http.ResponseWriter, r *http.Request, step CompletionStep) {
	w.Header().Set("Content-Type", "text/event-stream")
	chunks := step.SSEChunks
	if len(chunks) == 0 {
		chunks = completionStepStreamChunks(step)
	}
	if step.StallDuration > 0 && step.StallAfterChunks == 0 {
		flush(w)
		stall(r, step.StallDuration)
		return
	}
	for i, chunk := range chunks {
		if s.writeStreamChunk(w, r, step, chunk, i+1) {
			return
		}
	}
	if step.Usage != nil {
		s.writeUsageChunk(w, *step.Usage)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (s *Server) writeStreamChunk(w http.ResponseWriter, r *http.Request, step CompletionStep, chunk CompletionStreamChunk, number int) bool {
	b, err := json.Marshal(openAIStreamChunk(chunk))
	if err != nil {
		s.recordError("encode SSE chunk: %v", err)
		return true
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	flush(w)
	if step.AfterChunk != nil {
		step.AfterChunk(number)
	}
	if step.DropAfterChunks == number {
		dropConnection(w)
		return true
	}
	if step.StallDuration > 0 && step.StallAfterChunks == number {
		stall(r, step.StallDuration)
		return true
	}
	return false
}

// writeUsageChunk emits the trailing choice-less usage chunk a provider sends
// when stream usage is on — the only place a streamed response reports
// prompt_tokens_details.cached_tokens.
func (s *Server) writeUsageChunk(w http.ResponseWriter, usage StepUsage) {
	body, err := json.Marshal(map[string]any{
		"id": "chatcmpl-fixture", "object": "chat.completion.chunk", "created": 1,
		"model": "fixture-model", "choices": []map[string]any{}, "usage": usage.wire(),
	})
	if err != nil {
		s.recordError("encode usage chunk: %v", err)
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
	flush(w)
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func dropConnection(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hijacker.Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func stall(r *http.Request, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
	case <-timer.C:
	}
}

func (s *Server) nextCompletionStep(req CompletionRequest) (CompletionStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completionRequests = append(s.completionRequests, req)
	index := s.completionIndex
	s.completionIndex++
	if index < len(s.steps) {
		step := s.steps[index]
		if step.MutateForRequest != nil {
			step.MutateForRequest(req, &step)
		}
		return step, true
	}
	s.errors = append(s.errors, fmt.Sprintf("unexpected completion call %d", index+1))
	return CompletionStep{}, false
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.recordError("unexpected embedding method %s", r.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req EmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.recordError("decode embedding request: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.embeddingRequests = append(s.embeddingRequests, req)
	s.mu.Unlock()

	data := make([]map[string]any, 0, len(req.Input))
	for i := range req.Input {
		data = append(data, map[string]any{
			"object":    "embedding",
			"index":     i,
			"embedding": []float64{1, 0},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"model":  "memory-model",
		"data":   data,
		"usage":  map[string]any{"prompt_tokens": len(req.Input), "total_tokens": len(req.Input)},
	})
}

func (s *Server) recordError(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, fmt.Sprintf(format, args...))
}

func ReplyStep(message string) CompletionStep {
	return CompletionStep{Content: ReplyContent(message)}
}

func ReplyContent(message string) string {
	payload, err := json.Marshal(map[string]string{
		"type":    "reply",
		"message": message,
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func ToolCallStep(name string, args string) CompletionStep {
	return CompletionStep{
		Content: "<|tool_call>\n" +
			`{"name":"` + name + `","arguments":` + args + "}\n" +
			"<|tool_call|>",
	}
}

func NativeToolCallStep(id string, name string, args any) CompletionStep {
	return CompletionStep{ToolCalls: []CompletionToolCall{
		{
			ID:   id,
			Type: "function",
			Function: CompletionToolFunction{
				Name:      name,
				Arguments: args,
			},
		},
	}}
}

func ErrorStep(status int, body string) CompletionStep {
	return CompletionStep{Status: status, Body: body}
}

func SSEReplyStep(message string) CompletionStep {
	content := ReplyContent(message)
	mid := len(content) / 2
	return CompletionStep{SSEChunks: []CompletionStreamChunk{
		{Content: content[:mid]},
		{Content: content[mid:], Stop: true},
	}}
}

func completionStepStreamChunks(step CompletionStep) []CompletionStreamChunk {
	if len(step.ToolCalls) > 0 {
		return []CompletionStreamChunk{{ToolCalls: step.ToolCalls, Stop: true}}
	}
	return []CompletionStreamChunk{{Content: step.Content, Stop: true}}
}

func openAICompletionResponse(step CompletionStep) map[string]any {
	message := map[string]any{
		"role":    "assistant",
		"content": step.Content,
	}
	if len(step.ToolCalls) > 0 {
		message["tool_calls"] = step.ToolCalls
	}
	finishReason := "stop"
	if len(step.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	usage := StepUsage{PromptTokens: 1, CompletionTokens: 1}
	if step.Usage != nil {
		usage = *step.Usage
	}
	return map[string]any{
		"id":      "chatcmpl-fixture",
		"object":  "chat.completion",
		"created": 1,
		"model":   "fixture-model",
		"choices": []map[string]any{
			{
				"index":         0,
				"finish_reason": finishReason,
				"message":       message,
			},
		},
		"usage": usage.wire(),
	}
}

func openAIStreamChunk(chunk CompletionStreamChunk) map[string]any {
	delta := map[string]any{}
	if chunk.Content != "" {
		delta["content"] = chunk.Content
	}
	if len(chunk.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(chunk.ToolCalls))
		for index, call := range chunk.ToolCalls {
			calls = append(calls, map[string]any{
				"index":    index,
				"id":       call.ID,
				"type":     call.Type,
				"function": call.Function,
			})
		}
		delta["tool_calls"] = calls
	}
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if chunk.Stop {
		choice["finish_reason"] = "stop"
	}
	return map[string]any{
		"id":      "chatcmpl-fixture",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   "fixture-model",
		"choices": []map[string]any{choice},
	}
}
