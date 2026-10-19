package clients

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/config"
	inference "github.com/rivt-ai/go-inference-router"
	"github.com/rivt-ai/go-inference-router/provider/openaicompat"
)

// metadataPath is the llama.cpp model-metadata endpoint the adapter probes for
// the context window. Providers that do not serve it report a zero Metadata.
const metadataPath = "/props"

// Client adapts go-inference-router to the invokr-facing provider surface:
// streaming completions, model discovery, context-window metadata, and the
// summarize/embed helpers used by memory. Requests are routed through a
// router.Router, so which provider serves them is a configuration question
// rather than a build-time one.
type Client struct {
	BaseURL, APIKey string
	HTTPClient      *http.Client
	MaxRetries      int
	// StallTimeout bounds the wait between completion stream reads; zero
	// means the driver's default. Set it with SetStallTimeout, which rebuilds
	// the driver that captured it.
	StallTimeout time.Duration
	// Router is the provider table requests route through. The zero value
	// falls back to a single openai-compatible profile built from BaseURL and
	// APIKey, which is the local llama.cpp setup.
	Router RouterSpec

	driver *routerDriver
	// buildErr defers a bad provider table to the first call, so a
	// misconfigured profile surfaces as a request error rather than a panic
	// during composition.
	buildErr error
}

func NewClient(baseURL string) *Client { return NewClientWithSettings(baseURL, "", 10*time.Minute, 0) }

func NewClientWithSettings(baseURL, apiKey string, timeout time.Duration, maxRetries int) *Client {
	timeout = cmp.Or(timeout, openaicompat.DefaultHTTPTimeout)
	client := &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), APIKey: strings.TrimSpace(apiKey),
		HTTPClient: &http.Client{Timeout: timeout}, MaxRetries: maxRetries,
	}
	client.rebuild()
	return client
}

// NewRouterClient builds a client that routes through spec. Transport settings
// apply to in-process openai-compatible providers; a provider process owns its
// own transport.
func NewRouterClient(spec RouterSpec, timeout time.Duration, maxRetries int) *Client {
	timeout = cmp.Or(timeout, openaicompat.DefaultHTTPTimeout)
	client := &Client{HTTPClient: &http.Client{Timeout: timeout}, MaxRetries: maxRetries, Router: spec}
	client.rebuild()
	return client
}

// SetStallTimeout applies a new stall bound to subsequent requests.
func (c *Client) SetStallTimeout(d time.Duration) {
	c.StallTimeout = d
	c.rebuild()
}

// Close releases the router and any provider processes it started.
func (c *Client) Close() error {
	if c.driver == nil {
		return nil
	}
	return c.driver.Close()
}

// defaultSpec is the single-profile openai-compatible table the legacy
// BaseURL/APIKey constructors describe.
func (c *Client) defaultSpec() RouterSpec {
	return RouterSpec{
		Active: "default",
		Providers: map[string]RouterProvider{
			"default": {Type: compatProviderType, BaseURL: c.BaseURL, Options: map[string]any{"metadata_path": metadataPath}},
		},
		Profiles: map[string]RouterProfile{"default": {Provider: "default"}},
	}
}

// drv returns the routing driver, or a nil driver and no error when nothing is
// configured to route to. Callers that can degrade gracefully (model discovery,
// metadata) treat that as "not supported"; callers that cannot must error.
func (c *Client) drv() (*routerDriver, error) {
	if c.buildErr != nil {
		return nil, c.buildErr
	}
	if len(c.Router.Profiles) == 0 && c.BaseURL == "" {
		return nil, nil
	}
	return c.driver, nil
}

// requireDriver returns the routing driver or explains why there is none.
func (c *Client) requireDriver() (*routerDriver, error) {
	driver, err := c.drv()
	if err != nil {
		return nil, err
	}
	if driver == nil {
		return nil, fmt.Errorf("no provider is configured")
	}
	return driver, nil
}

func (c *Client) rebuild() {
	if c.driver != nil {
		_ = c.driver.Close()
		c.driver = nil
	}
	spec := c.Router
	if len(spec.Profiles) == 0 {
		spec = c.defaultSpec()
	}
	driver, err := newRouterDriver(spec, c.httpClient(), c.StallTimeout, c.APIKey)
	c.driver, c.buildErr = driver, err
}

// ListModels fetches the model list from the provider's GET /v1/models
// endpoint. Returns an empty slice (no error) when the provider responds
// with 404 or 405, so callers can treat an absent endpoint gracefully.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	driver, err := c.drv()
	if err != nil {
		return nil, err
	}
	if driver == nil {
		return nil, nil
	}
	models, err := driver.ListModels(ctx)
	if err != nil || len(models) == 0 {
		// A provider without a /v1/models endpoint yields no models and no
		// error; keep that as a nil slice so callers can tell "not supported"
		// apart from "supported but empty".
		return nil, err
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids, nil
}

// Metadata identifies the active provider's reasoning scale and, for servers
// that expose it, the context window. Anthropic is known from the provider
// type; OpenRouter from its host; llama.cpp from /props build_info (vLLM
// serves max_model_len there too); Ollama from /api/version when /props is
// absent. Nothing recognised yields a zero Metadata.
// ponytail: host/type sniffing; a config override comes when someone hits a proxy.
func (c *Client) Metadata(ctx context.Context, model string) (ProviderMetadata, error) {
	typ, base := c.activeProvider()
	if typ == "anthropic" {
		return ProviderMetadata{Reasoning: ReasoningAnthropic}, nil
	}
	if base == "" {
		return ProviderMetadata{}, nil
	}
	root := strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	out, found, err := c.probeProps(ctx, root, model)
	if err != nil {
		return ProviderMetadata{}, err
	}
	switch {
	case out.Reasoning != "":
	case isOpenRouter(root):
		out.Reasoning = ReasoningOpenRouter
	case !found && c.isOllama(ctx, root):
		out.Reasoning = ReasoningOllama
	}
	return out, nil
}

// probeProps reads llama.cpp's /props: the context window (vLLM's
// max_model_len too), build_info marking llama-server, and whether the chat
// template honours reasoning_effort. found is false when the endpoint is absent.
func (c *Client) probeProps(ctx context.Context, root, model string) (ProviderMetadata, bool, error) {
	endpoint := root + metadataPath
	if model = strings.TrimSpace(model); model != "" {
		endpoint += "?" + url.Values{"model": {model}}.Encode()
	}
	var props struct {
		Settings struct {
			NCtx int64 `json:"n_ctx"`
		} `json:"default_generation_settings"`
		MaxModelLen int64  `json:"max_model_len"`
		BuildInfo   string `json:"build_info"`
		Caps        struct {
			ReasoningEffort bool `json:"supports_reasoning_effort"`
			EnableThinking  bool `json:"supports_enable_thinking"`
		} `json:"chat_template_caps"`
		Modalities *struct {
			Vision bool `json:"vision"`
		} `json:"modalities"`
	}
	found, err := c.getJSON(ctx, endpoint, &props)
	if err != nil {
		return ProviderMetadata{}, false, err
	}
	var out ProviderMetadata
	if props.BuildInfo != "" {
		out.Reasoning, out.ReasoningEffort = ReasoningLlamaCpp, props.Caps.ReasoningEffort
		out.EnableThinking = props.Caps.EnableThinking
	}
	out.NoVision = props.Modalities != nil && !props.Modalities.Vision
	if out.ContextWindowTokens = max(props.Settings.NCtx, props.MaxModelLen); out.ContextWindowTokens > 0 {
		out.Source = "llama.cpp " + metadataPath
	}
	return out, found, nil
}

func isOpenRouter(root string) bool {
	u, err := url.Parse(root)
	return err == nil && strings.HasSuffix(u.Hostname(), "openrouter.ai")
}

// isOllama recognises Ollama by its version endpoint; failures read as "no".
func (c *Client) isOllama(ctx context.Context, root string) bool {
	var version struct {
		Version string `json:"version"`
	}
	ok, err := c.getJSON(ctx, root+"/api/version", &version)
	return err == nil && ok && version.Version != ""
}

// activeProvider is the type and base URL of the provider requests route to:
// the router's active profile, or the legacy BaseURL as openai-compatible.
func (c *Client) activeProvider() (typ, baseURL string) {
	if id, ok := c.Router.ActiveProfile(); ok {
		provider := c.Router.Providers[c.Router.Profiles[id].Provider]
		return provider.Type, strings.TrimSpace(provider.BaseURL)
	}
	return compatProviderType, strings.TrimSpace(c.BaseURL)
}

// getJSON GETs endpoint into out. found is false on 404/405 so optional
// endpoints are not failures; other non-2xx statuses are errors.
func (c *Client) getJSON(ctx context.Context, endpoint string, out any) (found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("%s: unexpected status %d", endpoint, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("%s: decode: %w", endpoint, err)
	}
	return true, nil
}

func (c *Client) Completion(ctx context.Context, req inference.Request) (*inference.Response, error) {
	return c.CompletionWithDeltas(ctx, req, nil)
}

// CompletionWithDeltas streams content fragments to onDelta while retaining
// Completion's accumulated response contract.
func (c *Client) CompletionWithDeltas(ctx context.Context, req inference.Request, onDelta func(string)) (*inference.Response, error) {
	// Once a fragment has reached the caller it has already been shown to the
	// user, so a retry would duplicate visible text. Retrying is therefore
	// confined to failures that happen before the first content delta.
	driver, err := c.requireDriver()
	if err != nil {
		return nil, err
	}
	var emitted bool
	return c.retry(ctx, func() bool { return !emitted }, func(ctx context.Context) (*inference.Response, error) {
		return driver.ChatStream(ctx, req, func(event inference.Event) error {
			if onDelta != nil && event.Kind == inference.EventContent && event.Text != "" {
				emitted = true
				onDelta(event.Text)
			}
			return nil
		})
	})
}

func (c *Client) CompletionOnce(ctx context.Context, req inference.Request) (*inference.Response, error) {
	driver, err := c.requireDriver()
	if err != nil {
		return nil, err
	}
	return c.retry(ctx, nil, func(ctx context.Context) (*inference.Response, error) {
		return driver.Chat(ctx, req)
	})
}

func (c *Client) Embeddings(ctx context.Context, req inference.EmbeddingRequest) ([][]float32, error) {
	driver, err := c.requireDriver()
	if err != nil {
		return nil, err
	}
	vectors, err := driver.Embed(ctx, req)
	return vectors, normalizeProviderError(err)
}

// httpClient returns the client requests are issued through, defaulting to a
// long timeout suited to slow local prompt eval.
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: openaicompat.DefaultHTTPTimeout}
	}
	return c.HTTPClient
}

func (c *Client) Summarize(ctx context.Context, input SummaryInput) (string, error) {
	if strings.TrimSpace(input.Prompt) == "" {
		return "", fmt.Errorf("summarize prompt is required")
	}

	req := inference.Request{Model: input.Model, Messages: []inference.Message{inference.UserMessage(input.Prompt)}}
	if input.JSON {
		req.ResponseFormat = inference.FormatJSON
	}
	if input.MaxTokens > 0 {
		req.MaxOutputTokens = input.MaxTokens
	}

	resp, err := c.CompletionOnce(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("llama summarize returned no response")
	}
	content := strings.TrimSpace(resp.Message.Content)
	if content == "" {
		return "", fmt.Errorf("llama summarize returned empty content")
	}
	return content, nil
}

func (c *Client) Embed(ctx context.Context, input EmbeddingInput) ([][]float32, error) {
	if len(input.Texts) == 0 {
		return nil, fmt.Errorf("llama embed texts are required")
	}

	vectors, err := c.Embeddings(ctx, inference.EmbeddingRequest{Texts: input.Texts})
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(input.Texts) {
		return nil, fmt.Errorf("llama embed returned %d vectors for %d texts", len(vectors), len(input.Texts))
	}
	return vectors, nil
}

// ApplyModelOptions overlays only explicit per-model settings on a main-turn
// request. Auxiliary requests keep their own budgets and sampling policy.
func ApplyModelOptions(req inference.Request, settings config.AgentConfig) (inference.Request, error) {
	for _, option := range config.ModelOptionRegistry {
		value, _ := option.Override(settings, req.Model)
		if value == "" {
			continue
		}
		if err := option.Validate(value); err != nil {
			return req, fmt.Errorf("model %q options: %w", req.Model, err)
		}
		option.Apply(&req, value)
	}
	return req, nil
}
