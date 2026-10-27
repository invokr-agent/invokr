package clients

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"

	inference "github.com/rivt-ai/go-inference-router"
	"github.com/rivt-ai/go-inference-router/router"
	routercfg "github.com/rivt-ai/go-inference-router/router/config"
)

// RouterSpec is the provider table a Client routes through: named provider
// definitions plus the named model profiles that select one. It is the
// host-owned shape, mapped onto the router's own config here so invokr keeps a
// single config file and a single precedence chain.
type RouterSpec struct {
	// Active names the profile requests resolve to. Empty selects the sole
	// profile when exactly one is defined.
	Active    string
	Providers map[string]RouterProvider
	Profiles  map[string]RouterProfile
}

// RouterProvider is one provider definition. Type "openai-compatible" is
// served in-process; every other type is an isolated provider process.
type RouterProvider struct {
	Type, BaseURL string
	// APIKeyEnv names an environment variable holding the credential; it wins
	// over APIKey, which stores one directly.
	APIKeyEnv, APIKey string
	// Path pins an explicit provider-process binary. Empty falls back to the
	// installer, then to a PATH lookup.
	Path, Version string
	Options       map[string]any
}

// RouterProfile binds a model to a provider.
type RouterProfile struct {
	Provider, Model string
}

// ActiveProfile reports the profile ID requests route to and whether one could
// be resolved — the named active profile, or the sole definition when none is
// named.
func (s RouterSpec) ActiveProfile() (string, bool) {
	id, err := s.resolveActive()
	return id, err == nil && id != ""
}

func (s RouterSpec) resolveActive() (string, error) {
	if s.Active != "" {
		if _, ok := s.Profiles[s.Active]; !ok {
			return "", fmt.Errorf("active profile %q is not defined", s.Active)
		}
		return s.Active, nil
	}
	if len(s.Profiles) != 1 {
		return "", fmt.Errorf("no active profile set and %d profiles are defined", len(s.Profiles))
	}
	for id := range s.Profiles {
		return id, nil
	}
	return "", nil
}

func (s RouterSpec) routerConfig() routercfg.Config {
	cfg := routercfg.Config{
		Version: routercfg.Version, Registry: registryTrust(), Providers: make(map[string]routercfg.Provider, len(s.Providers)),
		Models: make(map[string]routercfg.ModelProfile, len(s.Profiles)),
	}
	for id, p := range s.Providers {
		definition := routercfg.Provider{
			Type: p.Type, BaseURL: p.BaseURL, Path: p.Path, Version: p.Version, Options: p.Options,
		}
		switch {
		case p.APIKeyEnv != "":
			definition.Secrets = map[string]routercfg.SecretRef{"api_key": {Env: p.APIKeyEnv}}
		case p.APIKey != "":
			definition.Secrets = map[string]routercfg.SecretRef{"api_key": {Secret: p.APIKey}}
		}
		cfg.Providers[id] = definition
	}
	for id, profile := range s.Profiles {
		// A profile that pins no model is a provider selection only; the
		// model arrives with each request and its profile is registered then.
		// The router rejects a model-less profile outright.
		if profile.Model == "" {
			continue
		}
		cfg.Models[id] = routercfg.ModelProfile{Provider: profile.Provider, Model: profile.Model}
	}
	return cfg
}

// envSecrets is router.EnvResolver plus literal references: invokr maps a
// config-carried APIKey onto SecretRef.Secret directly, where upstream reads
// that field as an encrypted-store handle needing router/secret.
type envSecrets struct{ router.EnvResolver }

func (e envSecrets) Resolve(ref routercfg.SecretRef) (string, error) {
	if ref.Secret != "" {
		return ref.Secret, nil
	}
	return e.EnvResolver.Resolve(ref)
}

// compatProviderType is the router's built-in in-process provider type.
const compatProviderType = "openai-compatible"

// routerDriver adapts the profile-keyed Router to the model-keyed surface
// Client calls. Router pins a model per profile and overwrites Request.Model
// from it, but invokr resolves its model at runtime and can switch mid-session,
// so a request naming a model the table does not cover gets a profile
// registered for it on the fly. Adding a profile leaves open provider
// definitions untouched, so no provider process restarts.
type routerDriver struct {
	router *router.Router
	// active is the profile ID from the spec, used when a request names no
	// model of its own.
	active string
	// provider is the provider ID backing the active profile, which model
	// discovery is scoped to.
	provider string
	// activeModel is the model the active profile pins, empty when the spec
	// leaves the model to each request.
	activeModel string

	mu      sync.Mutex
	cfg     routercfg.Config
	derived map[string]string
}

func newRouterDriver(spec RouterSpec, httpClient *http.Client, stall time.Duration, literalKey string) (*routerDriver, error) {
	profileID, err := spec.resolveActive()
	if err != nil {
		return nil, err
	}
	cfg := spec.routerConfig()
	// literalKey is the credential of a provider that declares no secret
	// reference, which is how the legacy BaseURL/APIKey constructors supply
	// one. A provider table written by a user references an env var instead.
	if literalKey != "" {
		for id, definition := range cfg.Providers {
			if definition.Type == compatProviderType && len(definition.Secrets) == 0 {
				definition.Secrets = map[string]routercfg.SecretRef{"api_key": {Secret: literalKey}}
				cfg.Providers[id] = definition
			}
		}
	}
	// router.Open wires the verified-install locator, transport options, and
	// registry trust; a missing installer is not fatal there for the same
	// reason it was not here — in-process providers need no binary.
	r, err := router.Open(context.Background(), router.Options{
		Config: &cfg, Secrets: envSecrets{}, HTTPClient: httpClient, StallTimeout: stall,
	})
	if err != nil {
		return nil, fmt.Errorf("open router: %w", err)
	}
	return &routerDriver{
		router: r, active: profileID, cfg: cfg, derived: map[string]string{},
		provider: spec.Profiles[profileID].Provider, activeModel: spec.Profiles[profileID].Model,
	}, nil
}

func (d *routerDriver) profileFor(ctx context.Context, model string) (string, error) {
	if model == "" || model == d.activeModel {
		if d.activeModel == "" {
			return "", fmt.Errorf("profile %q pins no model and the request names none", d.active)
		}
		return d.active, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if id, ok := d.derived[model]; ok {
		return id, nil
	}
	id, next := d.provider+":"+model, cloneRouterConfig(d.cfg)
	next.Models[id] = routercfg.ModelProfile{Provider: d.provider, Model: model}
	if err := d.router.Apply(ctx, next); err != nil {
		return "", fmt.Errorf("register profile for model %q: %w", model, err)
	}
	d.cfg = next
	d.derived[model] = id
	return id, nil
}

func cloneRouterConfig(cfg routercfg.Config) routercfg.Config {
	clone := cfg
	clone.Providers, clone.Models = maps.Clone(cfg.Providers), maps.Clone(cfg.Models)
	if clone.Models == nil {
		clone.Models = map[string]routercfg.ModelProfile{}
	}
	return clone
}

func (d *routerDriver) Chat(ctx context.Context, req inference.Request) (*inference.Response, error) {
	return d.ChatStream(ctx, req, nil)
}

func (d *routerDriver) ChatStream(ctx context.Context, req inference.Request, onEvent func(inference.Event) error) (*inference.Response, error) {
	profile, err := d.profileFor(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	return d.router.Chat(ctx, profile, req, onEvent)
}

// Embed routes on the request's own model, which is typically the embedding
// model rather than the chat model the active profile pins.
func (d *routerDriver) Embed(ctx context.Context, req inference.EmbeddingRequest) ([][]float32, error) {
	profile, err := d.profileFor(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	return d.router.Embed(ctx, profile, req)
}

// ListModels reports the models the active profile's provider advertises.
func (d *routerDriver) ListModels(ctx context.Context) ([]inference.ModelInfo, error) {
	discovered, err := d.router.Discover(ctx, d.provider)
	if err != nil {
		return nil, err
	}
	models := make([]inference.ModelInfo, 0, len(discovered))
	for _, model := range discovered {
		models = append(models, model.Model)
	}
	return models, nil
}

func (d *routerDriver) Close() error { return d.router.Close() }
