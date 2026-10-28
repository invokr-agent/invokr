package web_search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/support/mathutil"
	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/tools/core"
)

type toolArgs struct {
	Query       string   `json:"query" desc:"Search query."`
	Providers   []string `json:"providers,omitempty" enum:"brave,tavily,custom_http,searxng" desc:"Optional providers: brave, tavily, custom_http, searxng."`
	Domains     []string `json:"domains,omitempty" desc:"Optional domains to constrain results."`
	RecencyDays int      `json:"recency_days,omitempty" desc:"Optional freshness window in days."`
	MaxResults  int      `json:"max_results,omitempty" desc:"Maximum merged results, capped at 20."`
}

type toolResponse struct {
	Results []SearchResult `json:"results"`
}

type searchService struct {
	cfg        config.WebSearchConfig
	dispatcher events.Dispatcher
	providers  map[string]Provider
	now        func() time.Time
}

// RegisterWebSearchTool registers web_search only when it is not disabled and
// at least one selected provider can actually be called; a tool the model can
// see but that fails on every call with "API key env is empty" is pure
// friction.
func RegisterWebSearchTool(c core.NativeRegistrar, deps core.CallerDeps) {
	if deps.App.WebSearch.Disabled {
		return
	}
	cfg := deps.App.WebSearch
	// Only the providers that can actually answer are named: a schema listing
	// brave and tavily with no keys set has the model asking for them and
	// reading back "API key env is empty" as if the query had no results.
	available := ConfiguredProviders(cfg)
	// searxng needs no key, so it stands in when every configured provider is
	// missing its credentials — even when the config never listed it. A config
	// naming only brave and tavily on a keyless box took the whole tool away,
	// and the model answered five search questions from memory instead
	// (r10 S1, needed INVOKR_WEB_SEARCH_PROVIDERS=searxng to come back).
	if len(available) == 0 && providerConfigured(constants.WebSearchProviderSearXNG, cfg) {
		available = []string{constants.WebSearchProviderSearXNG}
	}
	if len(available) == 0 {
		zap.L().Warn("web_search is not registered: no configured provider has credentials and searxng is unreachable", zap.Strings("providers", cfg.Providers))
		return
	}
	cfg.Providers = available
	service := NewSearchService(cfg, deps.Dispatcher, nil)
	core.Register(c, core.Tool[toolArgs]{
		Spec: core.ToolSpec{
			Name:        constants.WebSearchToolName,
			Description: "Search configured web providers, then merge, dedupe, rank, cache, and return sourced results. Uses web_network policy. Configured providers: " + strings.Join(available, ", ") + ".",
			Example:     `{"name":"web_search","arguments":{"query":"http caching best practices","providers":["` + available[0] + `"],"domains":["developer.mozilla.org"],"recency_days":7,"max_results":5}}`,
			Parameters:  core.SchemaFor[toolArgs](core.WithEnum("providers", available)),
			Metadata:    core.NetworkAskMetadata("network", constants.WebSearchPolicyTier, core.ToolCapabilities{Network: true}),
		},
		Prepare: func(ctx context.Context, args toolArgs) (toolArgs, error) {
			args = normalizeArgs(args, available)
			if args.Query == "" {
				return args, fmt.Errorf("query is required")
			}
			return args, nil
		},
		Approve: func(ctx context.Context, args toolArgs) core.ApprovalDecision {
			if !IsSensitiveTarget(args.Query, service.cfg) || service.cfg.AllowSensitive {
				return core.Auto()
			}
			return core.Ask(approvalpkgRequest(args))
		},
		Approver: deps.Approver,
		AuditFailure: func(ctx context.Context, args toolArgs, err error) {
			_, denied := errors.AsType[*approval.DeniedError](err)
			service.dispatchAudit(ctx, args, 0, false, IsSensitiveTarget(args.Query, service.cfg), map[bool]string{false: "not_approved", true: "denied"}[denied], 0, core.Outcome(err), err)
		},
		Execute: service.runArgs,
	})
}

func NewSearchService(cfg config.WebSearchConfig, dispatcher events.Dispatcher, providers map[string]Provider) *searchService {
	if providers == nil {
		client := &http.Client{Timeout: 30 * time.Second}
		providers = map[string]Provider{constants.WebSearchProviderBrave: NewBraveProvider(SearchProviderConfig{}, client), constants.WebSearchProviderTavily: NewTavilyProvider(SearchProviderConfig{}, client), constants.WebSearchProviderCustomHTTP: NewCustomHTTPProvider(cfg.CustomHTTP, client), constants.WebSearchProviderSearXNG: NewSearXNGProvider(cfg.SearXNG, cfg.CacheDir, client)}
	}
	return &searchService{cfg: cfg, dispatcher: dispatcher, providers: providers, now: time.Now}
}

func (s *searchService) runArgs(ctx context.Context, args toolArgs) (payload string, err error) {
	started := s.now()
	var resultCount int
	var cacheHit bool
	sensitive := IsSensitiveTarget(args.Query, s.cfg)
	approval := map[bool]string{false: constants.ApprovalPolicyAuto, true: map[bool]string{false: "approved", true: "preapproved"}[s.cfg.AllowSensitive]}[sensitive]
	defer func() {
		s.dispatchAudit(ctx, args, resultCount, cacheHit, sensitive, approval, s.now().Sub(started), core.Outcome(err), err)
	}()

	cache := newCache(s.cfg.CacheDir, s.now)
	if cached, ok := cache.Read(args); ok {
		cacheHit = true
		resultCount = len(cached.Results)
		return core.MarshalResult("web_search", cached)
	}

	providerResults, err := s.queryProviders(ctx, args)
	if err != nil {
		return "", err
	}
	results := rankResults(dedupeResults(providerResults), args.Query, s.now())
	results = results[:min(len(results), args.MaxResults)]
	for i := range results {
		results[i].Rank = i + 1
	}
	resp := toolResponse{Results: results}
	resultCount = len(results)
	_ = cache.Write(args, resp)
	return core.MarshalResult("web_search", resp)
}

func approvalpkgRequest(args toolArgs) approval.Request {
	data, _ := json.Marshal(args)
	return approval.Request{
		ReadOnly: true, Network: true,
		Tool: constants.WebSearchToolName, Tier: constants.WebSearchPolicyTier,
		Reason:  "query matched sensitive web search policy",
		Summary: "Search web for sensitive query",
		Details: fmt.Sprintf("Query: %s\nProviders: %s\nDomains: %s\nRecency days: %d\nMax results: %d\nRaw arguments: %s", args.Query, strings.Join(args.Providers, ", "), strings.Join(args.Domains, ", "), args.RecencyDays, args.MaxResults, string(data)),
	}
}

func (s *searchService) queryProviders(ctx context.Context, args toolArgs) ([]ProviderResult, error) {
	var (
		mu       sync.Mutex
		out      []ProviderResult
		failures []string
		wg       sync.WaitGroup
	)
	req := SearchRequest{Query: args.Query, Domains: args.Domains, RecencyDays: args.RecencyDays, MaxResults: args.MaxResults}
	for _, name := range args.Providers {
		provider := s.providers[name]
		if provider == nil {
			mu.Lock()
			failures = append(failures, fmt.Sprintf("%s: not configured", name))
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(name string, provider Provider) {
			defer wg.Done()
			results, err := provider.Search(ctx, req)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", name, err))
				return
			}
			out = append(out, results...)
		}(name, provider)
	}
	wg.Wait()
	if len(out) == 0 && len(failures) > 0 {
		return nil, fmt.Errorf("web_search providers failed: %s", strings.Join(failures, "; "))
	}
	return out, nil
}

func normalizeArgs(args toolArgs, available []string) toolArgs {
	args.Query = strings.TrimSpace(args.Query)
	// A provider the model named but that has no key answers nothing but an
	// error; drop it rather than report its failure as a search result.
	args.Providers = filterAvailable(core.NormalizeStrings(args.Providers, true), available)
	if len(args.Providers) == 0 {
		args.Providers = core.NormalizeStrings(available, true)
	}
	if len(args.Providers) == 0 {
		args.Providers = constants.DefaultWebSearchProviders()
	}
	args.Domains = core.NormalizeStrings(args.Domains, true)
	args.MaxResults = mathutil.ClampInt(args.MaxResults, constants.WebSearchDefaultMaxResults, constants.WebSearchMaxResultsCap)
	return args
}

// filterAvailable keeps only the requested providers that are configured. An
// empty available list means the caller has no roster to check against, so the
// request passes through unchanged.
func filterAvailable(requested, available []string) []string {
	if len(available) == 0 {
		return requested
	}
	return slices.DeleteFunc(requested, func(name string) bool { return !slices.Contains(available, name) })
}

func (s *searchService) dispatchAudit(ctx context.Context, args toolArgs, resultCount int, cacheHit bool, sensitive bool, approval string, duration time.Duration, outcome string, err error) {
	core.DispatchAudit(ctx, s.dispatcher, &events.AuditPayload{
		Action:  "web_search",
		Tool:    "web_search",
		Outcome: outcome,
		Details: map[string]any{"policy_tier": constants.WebSearchPolicyTier, "query_hash": strutil.HashHexTrimmed(args.Query), "providers": args.Providers, "domains": args.Domains, "recency_days": args.RecencyDays, "cache_hit": cacheHit, "sensitive_query": sensitive, "approval_decision": approval, "result_count": resultCount, "duration_ms": duration.Milliseconds()},
	}, err)
}

var (
	emailPattern  = regexp.MustCompile(`(?i)[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	tokenPattern  = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|bearer)\s*[:=]\s*[\w.\-]{8,}`)
	pathPattern   = regexp.MustCompile(`(?i)(^|\s)(/home/|/Users/|/tmp/|/[A-Za-z0-9._-]+/)`)
	secretPattern = regexp.MustCompile(`(?i)\b(secret|token|api key|private key|password)\b`)
)

// IsSensitiveTarget reports whether a query or URL trips the configured web
// policy (secret-looking text, sensitive terms, private hosts or repo names).
// Shared with web_fetch, which asks on the same web_network tier.
func IsSensitiveTarget(query string, cfg config.WebSearchConfig) bool {
	lower := strings.ToLower(query)
	if emailPattern.MatchString(query) || tokenPattern.MatchString(query) || pathPattern.MatchString(query) || secretPattern.MatchString(query) {
		return true
	}
	return containsSensitiveTerm(lower, cfg.SensitiveTerms) ||
		containsSensitiveTerm(lower, cfg.PrivateHosts) ||
		containsSensitiveTerm(lower, cfg.PrivateRepoNames)
}

func containsSensitiveTerm(lowerQuery string, values []string) bool {
	return slices.ContainsFunc(values, func(value string) bool {
		return value != "" && strings.Contains(lowerQuery, strings.ToLower(value))
	})
}

func ConfiguredProviders(cfg config.WebSearchConfig) []string {
	return slices.DeleteFunc(slices.Clone(cfg.Providers), func(name string) bool { return !providerConfigured(name, cfg) })
}

// providerConfigured reports whether one provider has what it needs to answer
// a search: a key for the hosted ones, an endpoint for custom_http, and for
// searxng either an endpoint or the container engine binary invokr starts it
// with (cfg.SearXNG.Engine, docker by default).
func providerConfigured(name string, cfg config.WebSearchConfig) bool {
	switch name {
	case constants.WebSearchProviderBrave:
		return os.Getenv(constants.BraveSearchAPIKeyEnv) != ""
	case constants.WebSearchProviderTavily:
		return os.Getenv(constants.TavilySearchAPIKeyEnv) != ""
	case constants.WebSearchProviderCustomHTTP:
		return strings.TrimSpace(cfg.CustomHTTP.Endpoint) != ""
	case constants.WebSearchProviderSearXNG:
		if strings.TrimSpace(cfg.SearXNG.Endpoint) != "" {
			return true
		}
		_, err := exec.LookPath(searxngEngine(cfg.SearXNG))
		return err == nil
	}
	return false
}
