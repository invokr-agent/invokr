package web_search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
)

// fakeTransport records the outgoing request (and its body) and replies with a
// canned response, so providers exercise a real *http.Client in tests. Each
// round trip gets a fresh response body, so one transport can serve several
// requests.
type fakeTransport struct {
	status   int
	respBody string
	err      error

	req  *http.Request
	body []byte
}

func (t *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.req = req
	if req.Body != nil {
		defer func() {
			_ = req.Body.Close()
		}()
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		t.body = body
	}
	if t.err != nil {
		return nil, t.err
	}
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.respBody)),
		Request:    req,
	}, nil
}

func (t *fakeTransport) client() *http.Client {
	return &http.Client{Transport: t}
}

func TestBraveResponseNormalizes(t *testing.T) {
	transport := &fakeTransport{respBody: `{
		"web": {
			"results": [
				{
					"title": " Go Release Notes ",
					"url": "https://www.go.dev/doc/go1.26",
					"description": "Latest release",
					"age": "2026-05-01",
					"score": 0.91
				}
			]
		}
	}`}
	provider := NewBraveProvider(SearchProviderConfig{
		Endpoint:     "https://brave.test/search",
		APIKeyEnv:    "BRAVE_TEST_KEY",
		APIKeyHeader: "X-Subscription-Token",
	}, transport.client())
	provider.apiKey = "secret"
	provider.now = fixedNow

	results, err := provider.Search(context.Background(), SearchRequest{
		Query:       "latest Go release notes",
		Domains:     []string{"go.dev"},
		RecencyDays: 7,
		MaxResults:  5,
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	got := results[0]
	if got.Title != "Go Release Notes" || got.URL != "https://www.go.dev/doc/go1.26" || got.Snippet != "Latest release" {
		t.Fatalf("unexpected normalized text: %#v", got)
	}
	if got.Provider != "brave" || got.Domain != "go.dev" || got.RetrievedAt != fixedNow() || got.Score != 0.91 {
		t.Fatalf("unexpected normalized metadata: %#v", got)
	}
	if got.PublishedAt == nil || got.PublishedAt.Format(time.DateOnly) != "2026-05-01" {
		t.Fatalf("expected published_at 2026-05-01, got %#v", got.PublishedAt)
	}
	if transport.req.Method != http.MethodGet || transport.req.Header.Get("X-Subscription-Token") != "secret" {
		t.Fatalf("unexpected brave request: %#v", transport.req)
	}
	parsed := transport.req.URL
	if parsed.Query().Get("count") != "5" || parsed.Query().Get("freshness") != "pw" {
		t.Fatalf("expected count and freshness query params, got %q", parsed.RawQuery)
	}
	if !strings.Contains(parsed.Query().Get("q"), "site:go.dev") {
		t.Fatalf("expected domain query in brave request, got %q", parsed.Query().Get("q"))
	}
}

func TestTavilyResponseNormalizes(t *testing.T) {
	transport := &fakeTransport{respBody: `{
		"results": [
			{
				"title": "Go 1.26",
				"url": "https://go.dev/blog/go1.26",
				"content": "Release blog",
				"published_date": "2026-05-02T10:00:00Z",
				"score": 0.87
			}
		]
	}`}
	provider := NewTavilyProvider(SearchProviderConfig{
		Endpoint:     "https://tavily.test/search",
		APIKeyEnv:    "TAVILY_TEST_KEY",
		APIKeyHeader: "Authorization",
	}, transport.client())
	provider.apiKey = "secret"
	provider.now = fixedNow

	results, err := provider.Search(context.Background(), SearchRequest{
		Query:      "latest Go release notes",
		Domains:    []string{"go.dev"},
		MaxResults: 5,
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	got := results[0]
	if got.Provider != "tavily" || got.Domain != "go.dev" || got.Title != "Go 1.26" || got.Snippet != "Release blog" || got.Score != 0.87 {
		t.Fatalf("unexpected tavily result: %#v", got)
	}
	if transport.req.Method != http.MethodPost || transport.req.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("unexpected tavily request: %#v", transport.req)
	}
	if !strings.Contains(string(transport.body), `"include_domains":["go.dev"]`) {
		t.Fatalf("expected include_domains in request body, got %s", string(transport.body))
	}
}

func TestCustomHTTPMappingWorks(t *testing.T) {
	transport := &fakeTransport{respBody: `{
		"items": [
			{
				"title": "Mapped title",
				"link": "https://example.com/a",
				"snippet": "Mapped snippet",
				"date": "2026-05-03",
				"score": 0.75
			}
		]
	}`}
	provider := NewCustomHTTPProvider(config.CustomHTTPSearchConfig{
		Endpoint:   "https://search.test/api",
		Method:     http.MethodGet,
		QueryParam: "q",
		Mappings: map[string]string{
			"results":      "$.items",
			"title":        "$.title",
			"url":          "$.link",
			"snippet":      "$.snippet",
			"published_at": "$.date",
			"score":        "$.score",
		},
	}, transport.client())
	provider.now = fixedNow

	results, err := provider.Search(context.Background(), SearchRequest{Query: "mapped"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	got := results[0]
	if got.Provider != "custom_http" || got.Domain != "example.com" || got.Title != "Mapped title" || got.URL != "https://example.com/a" {
		t.Fatalf("unexpected custom result: %#v", got)
	}
	if got.Snippet != "Mapped snippet" || got.Score != 0.75 {
		t.Fatalf("unexpected custom result fields: %#v", got)
	}
	parsed := transport.req.URL
	if parsed.Query().Get("q") != "mapped" {
		t.Fatalf("expected query param q=mapped, got %q", parsed.RawQuery)
	}
}

func TestProviderHTTPFailuresReturnErrors(t *testing.T) {
	transport := &fakeTransport{status: http.StatusTooManyRequests, respBody: "rate limited"}
	provider := NewBraveProvider(SearchProviderConfig{
		Endpoint:  "https://brave.test/search",
		APIKeyEnv: "BRAVE_TEST_KEY",
	}, transport.client())
	provider.apiKey = "secret"

	_, err := provider.Search(context.Background(), SearchRequest{Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("expected HTTP status error, got %v", err)
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
}

func TestJSONPathTrim(t *testing.T) {
	input := map[string]string{"$.items": "items", "$": "", "title": "title"}
	for in, want := range input {
		if got := trimJSONPath(in); got != want {
			t.Fatalf("trimJSONPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTavilyRequestBodyIsJSON(t *testing.T) {
	transport := &fakeTransport{respBody: `{"results":[]}`}
	provider := NewTavilyProvider(SearchProviderConfig{
		Endpoint:  "https://tavily.test/search",
		APIKeyEnv: "TAVILY_TEST_KEY",
	}, transport.client())
	provider.apiKey = "secret"

	_, err := provider.Search(context.Background(), SearchRequest{Query: "x", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(transport.body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if body["query"] != "x" || body["max_results"] != float64(3) {
		t.Fatalf("unexpected request body: %#v", body)
	}
}

// TestBraveFallsBackToPageAge covers the "|"-separated alternate path: Brave
// returns page_age when age is absent.
func TestBraveFallsBackToPageAge(t *testing.T) {
	transport := &fakeTransport{respBody: `{
		"web": {
			"results": [
				{
					"title": "Go 1.26",
					"url": "https://go.dev/doc/go1.26",
					"description": "Release notes",
					"page_age": "2026-05-01",
					"score": 0.5
				}
			]
		}
	}`}
	provider := NewBraveProvider(SearchProviderConfig{
		Endpoint:  "https://brave.test/search",
		APIKeyEnv: "BRAVE_TEST_KEY",
	}, transport.client())
	provider.apiKey = "secret"
	provider.now = fixedNow

	results, err := provider.Search(context.Background(), SearchRequest{Query: "go"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 || results[0].PublishedAt == nil {
		t.Fatalf("expected one result with PublishedAt, got %#v", results)
	}
	if got := results[0].PublishedAt.Format("2006-01-02"); got != "2026-05-01" {
		t.Fatalf("PublishedAt = %s, want 2026-05-01", got)
	}
}
