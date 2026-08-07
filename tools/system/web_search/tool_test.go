package web_search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools/core"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	name    string
	results []ProviderResult
	err     error
	calls   int
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Search(context.Context, SearchRequest) ([]ProviderResult, error) {
	p.calls++
	return p.results, p.err
}

type captureDispatcher struct {
	events []events.Event
}

func (d *captureDispatcher) Dispatch(_ context.Context, event events.Event) {
	d.events = append(d.events, event)
}

func TestMultipleProvidersMergeAndDedupeDuplicateURLs(t *testing.T) {
	now := fixedNow()
	brave := &fakeProvider{name: "brave", results: []ProviderResult{{
		Title:       "Go Release Notes",
		URL:         "https://go.dev/doc/go1.26?utm_source=x",
		Snippet:     "Release notes",
		Provider:    "brave",
		Domain:      "go.dev",
		PublishedAt: ptrTime(now.Add(-24 * time.Hour)),
		RetrievedAt: now,
		Score:       0.8,
	}}}
	tavily := &fakeProvider{name: "tavily", results: []ProviderResult{{
		Title:       "Go Release Notes",
		URL:         "https://www.go.dev/doc/go1.26",
		Snippet:     "Same result",
		Provider:    "tavily",
		Domain:      "go.dev",
		PublishedAt: ptrTime(now),
		RetrievedAt: now,
		Score:       0.9,
	}}}
	service := testService(t, map[string]Provider{"brave": brave, "tavily": tavily})
	service.now = func() time.Time { return now }

	resp := callSearch(t, service, `{"query":"Go release notes","providers":["brave","tavily"],"max_results":5}`)
	if len(resp.Results) != 1 {
		t.Fatalf("expected deduped result, got %#v", resp.Results)
	}
	got := resp.Results[0]
	if got.Provider != "tavily" || got.ProviderScores["brave"] != 0.8 || got.ProviderScores["tavily"] != 0.9 {
		t.Fatalf("expected merged provider scores, got %#v", got)
	}
	if !equalStrings(got.ProvidersReturnedBy, []string{"brave", "tavily"}) {
		t.Fatalf("expected both providers, got %#v", got.ProvidersReturnedBy)
	}
}

func TestSearchResultLimitBoundaries(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 1000} {
		provider := &fakeProvider{name: "brave", results: []ProviderResult{
			{Title: "one", URL: "https://one.example/result", Provider: "brave"},
			{Title: "two", URL: "https://two.example/result", Provider: "brave"},
		}}
		service := testService(t, map[string]Provider{"brave": provider})
		resp := callSearch(t, service, fmt.Sprintf(`{"query":"result","providers":["brave"],"max_results":%d}`, limit))
		want := 2
		if limit == 1 {
			want = 1
		}
		if len(resp.Results) != want {
			t.Fatalf("limit %d: got %d results, want %d", limit, len(resp.Results), want)
		}
	}
}

func TestSimilarTitlesDedupeOnSameDomain(t *testing.T) {
	now := fixedNow()
	results := []ProviderResult{
		{Title: "Go 1.26 Release Notes", URL: "https://go.dev/doc/go1.26", Provider: "brave", Domain: "go.dev", RetrievedAt: now, Score: 0.7},
		{Title: "Go 1.26 release notes", URL: "https://go.dev/blog/release", Provider: "tavily", Domain: "go.dev", RetrievedAt: now, Score: 0.6},
	}
	merged := dedupeResults(results)
	if len(merged) != 1 {
		t.Fatalf("expected similar titles to dedupe, got %#v", merged)
	}
}

func TestRankingPrefersFreshRelevantResult(t *testing.T) {
	now := fixedNow()
	results := []SearchResult{
		{Title: "Unrelated", Snippet: "misc", CanonicalURL: "https://a.test", PublishedAt: ptrTime(now.Add(-90 * 24 * time.Hour)), ProviderScores: map[string]float64{"brave": 0.9}, ProvidersReturnedBy: []string{"brave"}},
		{Title: "Latest Go release notes", Snippet: "Go release", CanonicalURL: "https://b.test", PublishedAt: ptrTime(now.Add(-2 * time.Hour)), ProviderScores: map[string]float64{"tavily": 0.7}, ProvidersReturnedBy: []string{"tavily"}},
	}
	ranked := rankResults(results, "Go release notes", now)
	if ranked[0].CanonicalURL != "https://b.test" {
		t.Fatalf("expected fresh relevant result first, got %#v", ranked)
	}
}

func TestCacheHitIsUsedAndTTLExpires(t *testing.T) {
	now := fixedNow()
	provider := &fakeProvider{name: "brave", results: []ProviderResult{{
		Title: "Live", URL: "https://example.com/live", Provider: "brave", Domain: "example.com", RetrievedAt: now, Score: 1,
	}}}
	service := testService(t, map[string]Provider{"brave": provider})
	service.now = func() time.Time { return now }
	args := `{"query":"cache me","providers":["brave"],"recency_days":7,"max_results":5}`
	first := callSearch(t, service, args)
	second := callSearch(t, service, args)
	if provider.calls != 1 || len(first.Results) != 1 || len(second.Results) != 1 {
		t.Fatalf("expected cache hit after first call, calls=%d first=%#v second=%#v", provider.calls, first, second)
	}
	service.now = func() time.Time { return now.Add(2 * time.Hour) }
	_ = callSearch(t, service, args)
	if provider.calls != 2 {
		t.Fatalf("expected expired cache to call provider again, calls=%d", provider.calls)
	}
}

func TestSensitiveQueryRequiresPreapproval(t *testing.T) {
	t.Setenv(constants.BraveSearchAPIKeyEnv, "key")
	registry := &fakeRegistrar{}
	RegisterWebSearchTool(registry, core.CallerDeps{App: config.AppConfig{WebSearch: config.WebSearchConfig{Providers: []string{"brave"}}}})
	_, err := registry.handler(context.Background(), &core.ToolCall{Name: "web_search", Arguments: json.RawMessage(`{"query":"api_key=secretsecret"}`)})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected core tool approval rejection, got %v", err)
	}
}

func TestProviderFailuresReturnPartialResults(t *testing.T) {
	now := fixedNow()
	service := testService(t, map[string]Provider{
		"brave":  &fakeProvider{name: "brave", err: errors.New("boom")},
		"tavily": &fakeProvider{name: "tavily", results: []ProviderResult{{Title: "ok", URL: "https://ok.test", Provider: "tavily", Domain: "ok.test", RetrievedAt: now, Score: 1}}},
	})
	service.now = func() time.Time { return now }
	resp := callSearch(t, service, `{"query":"ok","providers":["brave","tavily"]}`)
	if len(resp.Results) != 1 || resp.Results[0].Provider != "tavily" {
		t.Fatalf("expected partial result, got %#v", resp.Results)
	}
}

func TestAuditEventIsEmittedWithoutRawQuery(t *testing.T) {
	dispatcher := &captureDispatcher{}
	service := testService(t, map[string]Provider{"brave": &fakeProvider{name: "brave", results: []ProviderResult{{Title: "ok", URL: "https://ok.test", Provider: "brave", Domain: "ok.test", RetrievedAt: fixedNow(), Score: 1}}}})
	service.dispatcher = dispatcher
	_ = callSearch(t, service, `{"query":"private search","providers":["brave"],"domains":["go.dev"],"recency_days":1}`)
	if len(dispatcher.events) != 1 || dispatcher.events[0].Audit == nil {
		t.Fatalf("expected one audit event, got %#v", dispatcher.events)
	}
	audit := dispatcher.events[0].Audit
	if audit.Tool != "web_search" || audit.Action != "web_search" || audit.Details["policy_tier"] != constants.WebSearchPolicyTier || audit.Details["query_hash"] == "" {
		t.Fatalf("unexpected audit payload: %#v", audit)
	}
	data, _ := json.Marshal(audit)
	if strings.Contains(string(data), "private search") || strings.Contains(string(data), "ok.test") {
		t.Fatalf("audit leaked raw query/result data: %s", string(data))
	}
}

func TestApprovedSensitiveQueryAuditRecordsApproval(t *testing.T) {
	dispatcher := &captureDispatcher{}
	service := testService(t, map[string]Provider{"brave": &fakeProvider{name: "brave", results: []ProviderResult{{Title: "ok", URL: "https://ok.test", Provider: "brave", Domain: "ok.test", RetrievedAt: fixedNow(), Score: 1}}}})
	service.dispatcher = dispatcher
	_, err := service.runArgs(context.Background(), normalizeArgs(toolArgs{Query: "api_key=secretsecret", Providers: []string{"brave"}}, service.cfg.Providers))
	if err != nil {
		t.Fatalf("web_search failed: %v", err)
	}
	if len(dispatcher.events) != 1 || dispatcher.events[0].Audit == nil {
		t.Fatalf("expected one audit event, got %#v", dispatcher.events)
	}
	if got := dispatcher.events[0].Audit.Details["approval_decision"]; got != "approved" {
		t.Fatalf("approval_decision = %#v, want approved", got)
	}
}

func TestCallerRegistersWebSearchTool(t *testing.T) {
	t.Setenv(constants.TavilySearchAPIKeyEnv, "key")
	// Name tavily explicitly: the default list is searxng, whose gate depends
	// on a docker binary this test should not care about either way.
	deps := core.CallerDeps{App: config.AppConfig{}.WithDefaults()}
	deps.App.WebSearch.Providers = []string{constants.WebSearchProviderTavily}
	registry := &fakeRegistrar{}
	RegisterWebSearchTool(registry, deps)
	spec := registry.spec
	if spec.Name != constants.WebSearchToolName || spec.Metadata.Policy.Tiers[0] != constants.WebSearchPolicyTier || spec.Metadata.Policy.Approval != constants.ApprovalPolicyAsk {
		t.Fatalf("unexpected web_search spec: %#v", spec)
	}
}

type fakeRegistrar struct {
	spec    core.ToolSpec
	handler core.NativeHandler
}

func (r *fakeRegistrar) RegisterNative(spec core.ToolSpec, handler core.NativeHandler) {
	r.spec = spec
	r.handler = handler
}

func (r *fakeRegistrar) HasNative(string) bool { return false }

func (r *fakeRegistrar) NativeSource(string) string { return "" }

func (r *fakeRegistrar) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	return nil
}

func testService(t *testing.T, providers map[string]Provider) *searchService {
	t.Helper()
	return NewSearchService(config.WebSearchConfig{
		Providers: []string{"brave", "tavily"},
		CacheDir:  t.TempDir(),
	}, nil, providers)
}

func callSearch(t *testing.T, service *searchService, args string) toolResponse {
	t.Helper()
	out, err := runSearchToolCall(service, args)
	if err != nil {
		t.Fatalf("web_search failed: %v", err)
	}
	var resp toolResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	return resp
}

func ptrTime(value time.Time) *time.Time { return &value }

func runSearchToolCall(service *searchService, args string) (string, error) {
	decoded, err := core.DecodeOptionalToolArgs[toolArgs](&core.ToolCall{Name: "web_search", Arguments: json.RawMessage(args)})
	if err != nil {
		return "", err
	}
	normalized := normalizeArgs(decoded, service.cfg.Providers)
	if normalized.Query == "" {
		return "", errors.New("query is required")
	}
	return service.runArgs(context.Background(), normalized)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// With no provider key and no custom endpoint the tool is not offered at all:
// every call would fail with "API key env is empty" (#453).
func TestCallerSkipsWebSearchWithoutConfiguredProvider(t *testing.T) {
	t.Setenv(constants.BraveSearchAPIKeyEnv, "")
	t.Setenv(constants.TavilySearchAPIKeyEnv, "")

	// The default provider list is searxng, which needs no key, so the
	// tool-is-not-offered case is now an explicitly key-only list with no key
	// on a box where searxng cannot stand in for them either.
	deps := core.CallerDeps{App: config.AppConfig{}.WithDefaults()}
	deps.App.WebSearch.Providers = []string{constants.WebSearchProviderBrave, constants.WebSearchProviderTavily}
	deps.App.WebSearch.SearXNG.Engine = "invokr-no-such-engine"
	registry := &fakeRegistrar{}
	RegisterWebSearchTool(registry, deps)
	if registry.spec.Name != "" {
		t.Fatalf("web_search registered without a usable provider: %#v", registry.spec)
	}

	cfg := config.AppConfig{}.WithDefaults().WebSearch
	cfg.Providers = []string{constants.WebSearchProviderCustomHTTP}
	cfg.CustomHTTP.Endpoint = "http://search.local/q"
	if !HasConfiguredProvider(cfg) {
		t.Fatal("custom_http endpoint should count as configured")
	}

	// searxng is configured without any key: an explicit endpoint needs no
	// docker at all, and the default (container) mode needs only the binary.
	cfg = config.AppConfig{}.WithDefaults().WebSearch
	cfg.Providers = []string{constants.WebSearchProviderSearXNG}
	cfg.SearXNG.Endpoint = "http://127.0.0.1:8888/search"
	if !HasConfiguredProvider(cfg) {
		t.Fatal("searxng endpoint should count as configured")
	}
	cfg.SearXNG.Endpoint = ""
	if _, err := exec.LookPath(cfg.SearXNG.Engine); err == nil {
		if !HasConfiguredProvider(cfg) {
			t.Fatal("searxng should count as configured when the engine binary is present")
		}
	} else if HasConfiguredProvider(cfg) {
		t.Fatal("searxng should not count as configured with no endpoint and no engine binary")
	}
	// The gate follows the configured engine, not a hard-coded docker: a
	// binary that does not exist must not register the tool.
	cfg.SearXNG.Engine = "invokr-no-such-engine"
	if HasConfiguredProvider(cfg) {
		t.Fatal("searxng counted as configured with a missing engine binary")
	}
}

// The tool only names providers that are actually configured: with no brave
// or tavily key the schema, description, example, and the accepted argument
// must not offer them, or the model asks for them and reads back "API key env
// is empty" as though the query had no results.
func TestWebSearchOffersOnlyConfiguredProviders(t *testing.T) {
	t.Setenv(constants.BraveSearchAPIKeyEnv, "")
	t.Setenv(constants.TavilySearchAPIKeyEnv, "")

	deps := core.CallerDeps{App: config.AppConfig{}.WithDefaults()}
	deps.App.WebSearch.Providers = []string{
		constants.WebSearchProviderBrave,
		constants.WebSearchProviderTavily,
		constants.WebSearchProviderCustomHTTP,
	}
	deps.App.WebSearch.CustomHTTP.Endpoint = "http://search.local/q"
	registry := &fakeRegistrar{}
	RegisterWebSearchTool(registry, deps)
	if registry.spec.Name == "" {
		t.Fatal("web_search should register with a configured custom_http endpoint")
	}
	for _, unwanted := range []string{"brave", "tavily"} {
		if strings.Contains(registry.spec.Description, unwanted) {
			t.Fatalf("description advertises unconfigured %s: %q", unwanted, registry.spec.Description)
		}
		if strings.Contains(registry.spec.Example, unwanted) {
			t.Fatalf("example advertises unconfigured %s: %q", unwanted, registry.spec.Example)
		}
	}
	props, _ := registry.spec.Parameters["properties"].(map[string]any)
	providers, _ := props["providers"].(map[string]any)
	if got := fmt.Sprint(providers["enum"]); got != "[custom_http]" {
		t.Fatalf("providers enum = %v, want only the configured provider", providers["enum"])
	}

	// An unconfigured provider the model names anyway is dropped, not queried.
	args := normalizeArgs(toolArgs{Query: "q", Providers: []string{"brave"}}, ConfiguredProviders(deps.App.WebSearch))
	if !slices.Equal(args.Providers, []string{constants.WebSearchProviderCustomHTTP}) {
		t.Fatalf("providers = %v, want the unconfigured one dropped", args.Providers)
	}
}

// INVOKR_WEB_SEARCH_DISABLED is the opt-out for the default searxng provider,
// which would otherwise start a container on first search; it wins over any
// configured provider.
func TestCallerSkipsWebSearchWhenDisabled(t *testing.T) {
	t.Setenv(constants.TavilySearchAPIKeyEnv, "key")
	deps := core.CallerDeps{App: config.AppConfig{}.WithDefaults()}
	deps.App.WebSearch.Providers = []string{constants.WebSearchProviderTavily}
	deps.App.WebSearch.Disabled = true
	registry := &fakeRegistrar{}
	RegisterWebSearchTool(registry, deps)
	if registry.spec.Name != "" {
		t.Fatalf("web_search registered while disabled: %#v", registry.spec)
	}
}

// A config naming only keyless providers must not take web_search away when
// searxng can answer: r10 S1 answered five search questions from memory
// because brave and tavily were configured with no keys and searxng was not
// listed, and the tool went unregistered.
func TestCallerFallsBackToSearXNGWhenConfiguredProvidersLackKeys(t *testing.T) {
	t.Setenv(constants.BraveSearchAPIKeyEnv, "")
	t.Setenv(constants.TavilySearchAPIKeyEnv, "")

	deps := core.CallerDeps{App: config.AppConfig{}.WithDefaults()}
	deps.App.WebSearch.Providers = []string{constants.WebSearchProviderBrave, constants.WebSearchProviderTavily}
	deps.App.WebSearch.SearXNG.Endpoint = "http://127.0.0.1:8888/search"
	registry := &fakeRegistrar{}
	RegisterWebSearchTool(registry, deps)
	if registry.spec.Name != constants.WebSearchToolName {
		t.Fatal("web_search was not registered although searxng can answer")
	}
	if !strings.Contains(registry.spec.Description, constants.WebSearchProviderSearXNG) || strings.Contains(registry.spec.Description, constants.WebSearchProviderBrave) {
		t.Fatalf("the schema should name searxng alone: %s", registry.spec.Description)
	}
}

// A keyed provider still wins: searxng only stands in when nothing else can.
func TestCallerKeepsConfiguredProviderOverSearXNGFallback(t *testing.T) {
	t.Setenv(constants.BraveSearchAPIKeyEnv, "key")
	deps := core.CallerDeps{App: config.AppConfig{}.WithDefaults()}
	deps.App.WebSearch.Providers = []string{constants.WebSearchProviderBrave}
	registry := &fakeRegistrar{}
	RegisterWebSearchTool(registry, deps)
	if !strings.Contains(registry.spec.Description, constants.WebSearchProviderBrave) {
		t.Fatalf("a keyed provider should be kept: %s", registry.spec.Description)
	}
}
