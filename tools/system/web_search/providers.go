package web_search

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/strutil"

	"github.com/tidwall/gjson"
)

type SearchProviderConfig struct {
	Endpoint, APIKeyEnv, APIKeyHeader string
}

// jsonSearchProvider is the shape every JSON-over-HTTP search backend shares:
// resolve endpoint and API key, issue one request, check the status, decode the
// body, and normalize the items. Only the request shape and the field mapping
// differ per provider, so those are the two closures each constructor supplies.
type jsonSearchProvider struct {
	name     string
	endpoint string
	keyEnv   string
	client   *http.Client
	now      func() time.Time
	apiKey   string
	build    func(ctx context.Context, endpoint, apiKey string, req SearchRequest) (*http.Request, error)
	decode   func(body []byte) ([]providerItem, error)
	// retryBuild, when set, is used for one retry after retryDelay when the
	// first attempt failed — searxng uses it to ask a different engine set.
	retryBuild func(ctx context.Context, endpoint, apiKey string, req SearchRequest) (*http.Request, error)
	retryDelay time.Duration
}

// providerItem is one decoded search hit, before normalization.
type providerItem struct {
	Title, URL, Snippet, Published string
	Score                          float64
}

func (p *jsonSearchProvider) Name() string { return p.name }

func (p *jsonSearchProvider) Search(ctx context.Context, req SearchRequest) ([]ProviderResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.endpoint) == "" {
		return nil, fmt.Errorf("%s endpoint is required", p.name)
	}
	apiKey := strutil.FirstNonEmptyTrimmed(p.apiKey, os.Getenv(p.keyEnv))
	if p.keyEnv != "" && apiKey == "" {
		return nil, fmt.Errorf("%s API key env %q is empty", p.name, p.keyEnv)
	}
	results, err := p.searchOnce(ctx, apiKey, req, p.build)
	if err == nil || p.retryBuild == nil {
		return results, err
	}
	// A throttled instance recovers within seconds, so one retry with the
	// alternate request shape is worth more than a hard failure.
	select {
	case <-ctx.Done():
		return nil, err
	case <-time.After(p.retryDelay):
	}
	retried, retryErr := p.searchOnce(ctx, apiKey, req, p.retryBuild)
	if retryErr != nil {
		return nil, err
	}
	return retried, nil
}

func (p *jsonSearchProvider) searchOnce(ctx context.Context, apiKey string, req SearchRequest, build func(context.Context, string, string, SearchRequest) (*http.Request, error)) ([]ProviderResult, error) {
	request, err := build(ctx, strings.TrimSpace(p.endpoint), apiKey, req)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", p.name, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", p.name, err)
	}
	items, err := p.decode(body)
	if err != nil {
		return nil, err
	}
	return p.results(items), nil
}

func (p *jsonSearchProvider) results(items []providerItem) []ProviderResult {
	retrieved := p.now()
	out := make([]ProviderResult, 0, len(items))
	for _, item := range items {
		result := normalizeProviderResult(ProviderResult{Title: item.Title, URL: item.URL, Snippet: item.Snippet, Provider: p.name, PublishedAt: parseOptionalTime(item.Published), RetrievedAt: retrieved, Score: item.Score})
		if result.URL != "" {
			out = append(out, result)
		}
	}
	return out
}

func newProviderRequest(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req, nil
}

func newJSONProvider(name, endpoint, keyEnv string, client *http.Client) *jsonSearchProvider {
	return &jsonSearchProvider{name: name, endpoint: endpoint, keyEnv: keyEnv, client: cmp.Or(client, http.DefaultClient), now: time.Now, apiKey: os.Getenv(keyEnv)}
}

func NewBraveProvider(cfg SearchProviderConfig, client *http.Client) *jsonSearchProvider {
	cfg.Endpoint = strutil.FirstNonEmptyTrimmed(cfg.Endpoint, constants.BraveSearchEndpoint)
	cfg.APIKeyEnv = strutil.FirstNonEmptyTrimmed(cfg.APIKeyEnv, constants.BraveSearchAPIKeyEnv)
	keyHeader := strutil.FirstNonEmptyTrimmed(cfg.APIKeyHeader, constants.BraveSearchAPIKeyHeader, "X-Subscription-Token")
	p := newJSONProvider("brave", cfg.Endpoint, cfg.APIKeyEnv, client)
	p.build = func(ctx context.Context, endpoint, apiKey string, req SearchRequest) (*http.Request, error) {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse brave endpoint: %w", err)
		}
		q := u.Query()
		q.Set("q", braveQuery(req))
		if req.MaxResults > 0 {
			q.Set("count", strconv.Itoa(req.MaxResults))
		}
		if req.RecencyDays > 0 {
			q.Set("freshness", braveFreshness(req.RecencyDays))
		}
		u.RawQuery = q.Encode()
		return newProviderRequest(ctx, http.MethodGet, u.String(), nil, map[string]string{keyHeader: apiKey, "Accept": "application/json", "User-Agent": "github.com/invokr-ai/invokr"})
	}
	p.decode = mappedDecoder("brave", map[string]string{"results": "$.web.results", "title": "$.title", "url": "$.url", "snippet": "$.description", "published_at": "$.age|$.page_age", "score": "$.score"})
	return p
}

func NewTavilyProvider(cfg SearchProviderConfig, client *http.Client) *jsonSearchProvider {
	cfg.Endpoint = strutil.FirstNonEmptyTrimmed(cfg.Endpoint, constants.TavilySearchEndpoint)
	cfg.APIKeyEnv = strutil.FirstNonEmptyTrimmed(cfg.APIKeyEnv, constants.TavilySearchAPIKeyEnv)
	keyHeader := strutil.FirstNonEmptyTrimmed(cfg.APIKeyHeader, constants.TavilySearchAPIKeyHeader, "Authorization")
	p := newJSONProvider("tavily", cfg.Endpoint, cfg.APIKeyEnv, client)
	p.build = func(ctx context.Context, endpoint, apiKey string, req SearchRequest) (*http.Request, error) {
		body := map[string]any{"query": req.Query, "max_results": req.MaxResults}
		if len(req.Domains) > 0 {
			body["include_domains"] = req.Domains
		}
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return newProviderRequest(ctx, http.MethodPost, endpoint, data, map[string]string{keyHeader: "Bearer " + apiKey, "Content-Type": "application/json", "Accept": "application/json"})
	}
	p.decode = mappedDecoder("tavily", map[string]string{"results": "$.results", "title": "$.title", "url": "$.url", "snippet": "$.content", "published_at": "$.published_date", "score": "$.score"})
	return p
}

func NewCustomHTTPProvider(cfg config.CustomHTTPSearchConfig, client *http.Client) *jsonSearchProvider {
	method := strings.ToUpper(strutil.FirstNonEmptyTrimmed(cfg.Method, http.MethodGet))
	queryParam := strutil.FirstNonEmptyTrimmed(cfg.QueryParam, "q")
	p := newJSONProvider("custom_http", cfg.Endpoint, "", client)
	p.build = func(ctx context.Context, endpoint, _ string, req SearchRequest) (*http.Request, error) {
		return buildCustomHTTPRequest(ctx, cfg, method, queryParam, endpoint, req)
	}
	p.decode = mappedDecoder("custom_http", cfg.Mappings)
	return p
}

func buildCustomHTTPRequest(ctx context.Context, cfg config.CustomHTTPSearchConfig, method, queryParam, endpoint string, req SearchRequest) (*http.Request, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse custom_http endpoint: %w", err)
	}
	headers := map[string]string{"Accept": "application/json"}
	var body []byte
	if method == http.MethodGet {
		q := u.Query()
		q.Set(queryParam, req.Query)
		u.RawQuery = q.Encode()
	} else {
		if body, err = json.Marshal(map[string]any{queryParam: req.Query}); err != nil {
			return nil, err
		}
		headers["Content-Type"] = "application/json"
	}
	for header, envName := range cfg.HeadersEnv {
		if value := os.Getenv(envName); value != "" {
			headers[header] = value
		}
	}
	return newProviderRequest(ctx, method, u.String(), body, headers)
}

// mappedDecoder decodes a provider response through JSON paths. Every backend
// here returns a list of objects with the same six fields under different
// names, so the paths are all that distinguishes them. A "published_at" path
// may list "|"-separated alternates, tried in order.
func mappedDecoder(name string, mappings map[string]string) func([]byte) ([]providerItem, error) {
	if len(mappings) == 0 {
		mappings = constants.DefaultCustomHTTPSearchMappings()
	}
	return func(body []byte) ([]providerItem, error) {
		root := gjson.GetBytes(body, trimJSONPath(mappings["results"]))
		if !root.Exists() {
			return nil, fmt.Errorf("%s response missing results mapping %q", name, mappings["results"])
		}
		var items []providerItem
		root.ForEach(func(_, value gjson.Result) bool {
			items = append(items, providerItem{Title: getMappedString(value, mappings["title"]), URL: getMappedString(value, mappings["url"]), Snippet: getMappedString(value, mappings["snippet"]), Published: firstMappedString(value, mappings["published_at"]), Score: value.Get(trimJSONPath(mappings["score"])).Float()})
			return true
		})
		return items, nil
	}
}

func firstMappedString(root gjson.Result, paths string) string {
	for _, path := range strings.Split(paths, "|") {
		if value := getMappedString(root, path); value != "" {
			return value
		}
	}
	return ""
}

func braveQuery(req SearchRequest) string {
	if len(req.Domains) == 0 {
		return req.Query
	}
	return req.Query + " " + domainQuery(req.Domains)
}

func normalizeProviderResult(result ProviderResult) ProviderResult {
	result.Title = strings.TrimSpace(result.Title)
	result.URL = strings.TrimSpace(result.URL)
	result.Snippet = strings.TrimSpace(result.Snippet)
	result.Provider = strings.TrimSpace(result.Provider)
	if result.Domain == "" {
		result.Domain = domainFromURL(result.URL)
	}
	return result
}

func domainFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	return strings.TrimPrefix(host, "www.")
}

func parseOptionalTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	layouts := []string{time.RFC3339, "2006-01-02", "2006-01-02T15:04:05Z0700"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return &t
		}
	}
	return nil
}

func getMappedString(root gjson.Result, path string) string {
	return strings.TrimSpace(root.Get(trimJSONPath(path)).String())
}

func trimJSONPath(path string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(path), "$."), "$")
}

func braveFreshness(days int) string { return recencyBucket(days, "pd", "pw", "pm", "py") }

// recencyBucket maps a days window onto a provider's freshness tokens.
func recencyBucket(days int, day, week, month, year string) string {
	switch {
	case days <= 1:
		return day
	case days <= 7:
		return week
	case days <= 31:
		return month
	}
	return year
}

func domainQuery(domains []string) string {
	parts := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain != "" {
			parts = append(parts, "site:"+domain)
		}
	}
	return strings.Join(parts, " OR ")
}
