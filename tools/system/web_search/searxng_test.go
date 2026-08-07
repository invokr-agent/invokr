package web_search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

func TestSearXNGResponseNormalizes(t *testing.T) {
	transport := &fakeTransport{respBody: `{
		"results": [
			{"title": "Go 1.26", "url": "https://go.dev/blog/go1.26", "content": "Release blog", "publishedDate": "2026-05-02T10:00:00Z", "score": 1.5, "engine": "duckduckgo"}
		]
	}`}
	provider := NewSearXNGProvider(config.SearXNGSearchConfig{Endpoint: "http://searx.test/search"}, "", transport.client())
	provider.now = fixedNow

	results, err := provider.Search(context.Background(), SearchRequest{Query: "go release", Domains: []string{"go.dev"}, RecencyDays: 5})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	got := results[0]
	if got.Provider != "searxng" || got.Domain != "go.dev" || got.Title != "Go 1.26" || got.Snippet != "Release blog" || got.Score != 1.5 || got.PublishedAt == nil {
		t.Fatalf("unexpected searxng result: %#v", got)
	}
	q := transport.req.URL.Query()
	if transport.req.Method != http.MethodGet || q.Get("format") != "json" || q.Get("time_range") != "week" || q.Get("q") != "go release site:go.dev" {
		t.Fatalf("unexpected searxng request: %s", transport.req.URL)
	}
}

func TestSearXNGRunArgvAndSettings(t *testing.T) {
	settings, err := writeSearXNGSettings(filepath.Join(t.TempDir(), "searxng", "settings.yml"))
	if err != nil {
		t.Fatalf("write settings: %v", err)
	}
	argv := strings.Join(searxngRunArgv(config.SearXNGSearchConfig{Port: 8888, Image: "searxng/searxng:latest"}, settings), " ")
	want := "run -d --name invokr-searxng -p 127.0.0.1:8888:8080 -v " + settings + ":/etc/searxng/settings.yml:ro searxng/searxng:latest"
	if argv != want {
		t.Fatalf("argv = %q, want %q", argv, want)
	}

	rendered, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	for _, engine := range []string{"bing", "yandex", "seznam"} {
		if !strings.Contains(string(rendered), "name: "+engine) {
			t.Fatalf("rendered settings missing enabled engine %q:\n%s", engine, rendered)
		}
	}
}

func TestSearXNGDecoderFailsWhenAllEnginesSuspended(t *testing.T) {
	body := []byte(`{"results":[],"unresponsive_engines":[["brave","Suspended: too many requests"],["duckduckgo","Suspended: access denied"]]}`)
	if _, err := searxngDecoder()(body); err == nil || !strings.Contains(err.Error(), "brave: Suspended") {
		t.Fatalf("want suspended-engines error, got %v", err)
	}
	if items, err := searxngDecoder()([]byte(`{"results":[],"unresponsive_engines":[]}`)); err != nil || len(items) != 0 {
		t.Fatalf("empty results without suspensions must stay a plain empty set, got %v %v", items, err)
	}
}

// A throttled first response is retried once against the fallback engine set
// rather than reported as a hard failure.
func TestSearXNGRetriesThrottledResponseWithFallbackEngines(t *testing.T) {
	var engines []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engines = append(engines, r.URL.Query().Get("engines"))
		if len(engines) == 1 {
			_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[["bing","Suspended: too many requests"]]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"Go 1.26","url":"https://go.dev/blog/go1.26","content":"Release blog"}]}`))
	}))
	defer server.Close()

	provider := NewSearXNGProvider(config.SearXNGSearchConfig{Endpoint: server.URL}, "", server.Client())
	provider.now = fixedNow
	provider.retryDelay = 0

	results, err := provider.Search(context.Background(), SearchRequest{Query: "go release"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Go 1.26" {
		t.Fatalf("unexpected results: %#v", results)
	}
	if len(engines) != 2 || engines[0] != searxngEngines || engines[1] != searxngFallbackEngines {
		t.Fatalf("engine sets = %v, want [%s %s]", engines, searxngEngines, searxngFallbackEngines)
	}
}

// The throttle error must read as transient and suggest rephrasing.
func TestSearXNGThrottleErrorReadsAsTransient(t *testing.T) {
	_, err := searxngDecoder()([]byte(`{"results":[],"unresponsive_engines":[["brave","Suspended: too many requests"]]}`))
	if err == nil {
		t.Fatal("want throttle error")
	}
	for _, want := range []string{"transient throttle", "different keywords"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("throttle error missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("throttle error still tells the model to stop searching: %v", err)
	}
}
