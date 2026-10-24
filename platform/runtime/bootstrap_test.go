package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/turn"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// modelsResponse is the /v1/models JSON shape used by the fake server.
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func modelsServer(models []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		resp := modelsResponse{}
		for _, m := range models {
			resp.Data = append(resp.Data, struct {
				ID string `json:"id"`
			}{ID: m})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestResolveModel_ConfiguredPresent(t *testing.T) {
	srv := modelsServer([]string{"model-a", "model-b"})
	defer srv.Close()
	c := clients.NewClient(srv.URL)
	got, _ := resolveModel(context.Background(), c, "model-a", zap.NewNop())
	if got != "model-a" {
		t.Fatalf("expected model-a, got %s", got)
	}
}

func TestResolveModel_ConfiguredAbsent(t *testing.T) {
	srv := modelsServer([]string{"model-x", "model-y"})
	defer srv.Close()
	c := clients.NewClient(srv.URL)
	got, _ := resolveModel(context.Background(), c, "model-z", zap.NewNop())
	if got != "model-x" {
		t.Fatalf("expected fallback model-x, got %s", got)
	}
}

func TestResolveModel_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := clients.NewClient(srv.URL)
	got, err := resolveModel(context.Background(), c, "my-model", zap.NewNop())
	if got != "my-model" || err != nil {
		t.Fatalf("expected unchanged my-model and no problem on 404, got %s, %v", got, err)
	}
}

func TestResolveModel_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "oops", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := clients.NewClient(srv.URL)
	got, _ := resolveModel(context.Background(), c, "my-model", zap.NewNop())
	if got != "my-model" {
		t.Fatalf("expected unchanged my-model on server error, got %s", got)
	}
}

func TestResolveModel_Unreachable(t *testing.T) {
	c := clients.NewClient("http://127.0.0.1:1") // nothing listening
	got, err := resolveModel(context.Background(), c, "my-model", zap.NewNop())
	if got != "my-model" {
		t.Fatalf("expected unchanged my-model when unreachable, got %s", got)
	}
	if !strings.Contains(clients.DescribeError(err), "Can't reach the model server at 127.0.0.1:1") {
		t.Fatalf("unreachable probe error = %v, want it reported", err)
	}
}

func TestResolveModel_EmptyList(t *testing.T) {
	srv := modelsServer([]string{})
	defer srv.Close()
	c := clients.NewClient(srv.URL)
	got, _ := resolveModel(context.Background(), c, "my-model", zap.NewNop())
	if got != "my-model" {
		t.Fatalf("expected unchanged my-model on empty list, got %s", got)
	}
}

func TestNewProviderDoesNotProbeModelsSynchronously(t *testing.T) {
	var modelRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			modelRequests.Add(1)
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	app := config.AppConfig{Provider: config.ProviderConfig{BaseURL: srv.URL, Model: "configured"}}.WithDefaults()
	_, got := newProvider(app, Options{}, zap.NewNop())
	if got.Provider.Model != "configured" {
		t.Fatalf("model = %q, want configured", got.Provider.Model)
	}
	if modelRequests.Load() != 0 {
		t.Fatalf("startup model requests = %d, want 0", modelRequests.Load())
	}
}

func TestBootstrapInitializesMemoryAndCleanupIsSafe(t *testing.T) {
	root := t.TempDir()
	app := config.AppConfig{
		Provider: config.ProviderConfig{Model: "test-model"},
		Memory: config.MemoryConfig{
			Dir: filepath.Join(root),
		},
		Toolbox: config.ToolboxConfig{
			Dir: filepath.Join(root, "toolbox"),
		},
	}

	rt, err := Bootstrap(app, Options{})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	services := rt.Services()
	if services.App.Memory.Dir == "" {
		t.Fatal("Bootstrap() did not expose app config")
	}
	if services.Logger == nil {
		t.Fatal("Bootstrap() did not initialize logger")
	}
	if services.Session == nil {
		t.Fatal("Bootstrap() did not initialize agent session")
	}
	if services.Events == nil {
		t.Fatal("Bootstrap() did not initialize event bus")
	}
	if services.PromptHistory == nil {
		t.Fatal("Bootstrap() did not initialize prompt history")
	}
	services.Session = nil
	if rt.Services().Session == nil {
		t.Fatal("Services() returned mutable runtime services")
	}

	if err := rt.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if err := rt.Cleanup(); err != nil {
		t.Fatalf("second Cleanup() error = %v", err)
	}
}

func TestBootstrapUsesProviderContextWindowMetadata(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":131072}}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{\\\"type\\\":\\\"reply\\\",\\\"message\\\":\\\"ok\\\"}\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	app := config.AppConfig{
		Provider: config.ProviderConfig{
			BaseURL: server.URL,
			Model:   "test-model",
		},
		Memory: config.MemoryConfig{
			Dir: filepath.Join(root),
		},
		Toolbox: config.ToolboxConfig{
			Dir: filepath.Join(root, "toolbox"),
		},
	}

	rt, err := Bootstrap(app, Options{})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	defer func() {
		_ = rt.Cleanup()
	}()

	// Metadata discovery starts in the background; sending immediately can
	// finish before /props has updated the shared provider context.
	usage := rt.Services().Session.(interface{ Usage() turn.TokenUsage })
	deadline := time.Now().Add(2 * time.Second)
	for usage.Usage().ContextWindowTokens != 131072 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := usage.Usage().ContextWindowTokens; got != 131072 {
		t.Fatalf("provider metadata window = %d, want 131072", got)
	}

	resp, err := rt.Services().Session.Send(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if want := int64(131072); resp.Usage.ContextWindowTokens != want {
		t.Fatalf("expected the API context window, got %d want %d", resp.Usage.ContextWindowTokens, want)
	}
}

func TestWithProfileSampling(t *testing.T) {
	t.Parallel()
	local := config.RouterProviderConfig{Type: "openai-compatible"}
	anthropic := config.RouterProviderConfig{Type: "anthropic"}

	for _, tc := range []struct {
		name     string
		agent    config.AgentConfig
		provider config.RouterProviderConfig
		want     bool
	}{
		{"local keeps temperature", config.AgentConfig{}, local, false},
		{"anthropic drops temperature", config.AgentConfig{}, anthropic, true},
		{
			name:     "explicit opt-out survives a local profile",
			agent:    config.AgentConfig{TemperatureDisabled: true},
			provider: local,
			want:     true,
		},
		{
			name:     "provider option opts in any type",
			provider: config.RouterProviderConfig{Type: "openai", Options: map[string]any{"temperature_disabled": true}},
			want:     true,
		},
		{
			name:     "provider option overrides the built-in list",
			provider: config.RouterProviderConfig{Type: "anthropic", Options: map[string]any{"temperature_disabled": false}},
			want:     false,
		},
	} {
		if got := withProfileSampling(tc.agent, tc.provider).TemperatureDisabled; got != tc.want {
			t.Errorf("%s: TemperatureDisabled = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The --log-filter flag's help text advertises "agent.session=<level>" as a
// working example; that only means anything if a logger by that name exists.
func TestSessionDepsLoggerIsNamedAgentSession(t *testing.T) {
	deps := toolLayerDeps{logger: zap.NewNop()}
	sessDeps := deps.sessionDeps("sess-1", nil, nil)
	if got, want := sessDeps.Logger.Name(), "agent.session"; got != want {
		t.Fatalf("session logger name = %q, want %q", got, want)
	}
}

// chatModelServer records the model of every /v1/chat/completions request and
// answers with a minimal completion.
func chatModelServer(seen *atomic.Pointer[string]) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		model := body.Model
		seen.Store(&model)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
}

func routerPinnedConfig(baseURL string) config.AppConfig {
	return config.AppConfig{Provider: config.ProviderConfig{Router: config.RouterConfig{
		Active:    "local",
		Providers: map[string]config.RouterProviderConfig{"local": {Type: "openai-compatible", BaseURL: baseURL}},
		Profiles:  map[string]config.RouterProfileConfig{"local": {Provider: "local", Model: "pinned-model"}},
	}}}.WithDefaults()
}

// An explicit INVOKR_MODEL must beat the active router profile's pin, and the
// request must actually carry it — a run that reports one model and calls
// another mis-attributes everything downstream.
func TestNewProviderExplicitEnvModelBeatsProfilePin(t *testing.T) {
	var seen atomic.Pointer[string]
	srv := chatModelServer(&seen)
	defer srv.Close()
	t.Setenv("INVOKR_MODEL", "explicit-model")

	app := routerPinnedConfig(srv.URL)
	app.Provider.Model = "explicit-model" // env binding does this in config load
	provider, got := newProvider(app, Options{}, zap.NewNop())
	defer func() { _ = provider.Close() }()
	if got.Provider.Model != "explicit-model" {
		t.Fatalf("model = %q, want explicit-model", got.Provider.Model)
	}
	if _, err := provider.CompletionOnce(context.Background(), inference.Request{
		Model:    got.Provider.Model,
		Messages: []inference.Message{inference.UserMessage("hi")},
	}); err != nil {
		t.Fatalf("completion: %v", err)
	}
	if sent := seen.Load(); sent == nil || *sent != "explicit-model" {
		t.Fatalf("request model = %v, want explicit-model", sent)
	}
}

func TestNewProviderProfilePinAppliesWithoutExplicitModel(t *testing.T) {
	var seen atomic.Pointer[string]
	srv := chatModelServer(&seen)
	defer srv.Close()
	t.Setenv("INVOKR_MODEL", "")

	provider, got := newProvider(routerPinnedConfig(srv.URL), Options{}, zap.NewNop())
	defer func() { _ = provider.Close() }()
	if got.Provider.Model != "pinned-model" {
		t.Fatalf("model = %q, want pinned-model", got.Provider.Model)
	}
}

func TestNewProviderModelFlagBeatsProfilePin(t *testing.T) {
	var seen atomic.Pointer[string]
	srv := chatModelServer(&seen)
	defer srv.Close()
	t.Setenv("INVOKR_MODEL", "")

	app := routerPinnedConfig(srv.URL)
	app.Provider.Model = "flag-model"
	provider, got := newProvider(app, Options{ModelFlagSet: true}, zap.NewNop())
	defer func() { _ = provider.Close() }()
	if got.Provider.Model != "flag-model" {
		t.Fatalf("model = %q, want flag-model", got.Provider.Model)
	}
}

func TestBootstrapFailsWithoutAnyModel(t *testing.T) {
	srv := modelsServer(nil)
	defer srv.Close()
	root := t.TempDir()
	app := config.AppConfig{Provider: config.ProviderConfig{BaseURL: srv.URL}, Memory: config.MemoryConfig{Dir: root}}
	if _, err := Bootstrap(app, Options{}); err == nil || !strings.Contains(err.Error(), "no model configured") {
		t.Fatalf("Bootstrap() error = %v, want no-model error", err)
	}
}
