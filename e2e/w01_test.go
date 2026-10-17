//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// webFixture serves the hermetic pages W01 fetches and counts every request it
// receives, so a "no request was made" oracle is an assertion, not a guess.
type webFixture struct {
	server *httptest.Server
	hits   atomic.Int64
}

func newWebFixture(t *testing.T) *webFixture {
	t.Helper()
	fixture := &webFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/page.html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><head><style>b{}</style></head><body><h1>Fixture Page</h1><p>codename &#39;amber-orbit-page&#39;</p><script>leak()</script></body></html>")
	})
	mux.HandleFunc("/big.html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<p>head-of-big-page</p>"+strings.Repeat("<p>filler</p>", 4000))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "fixture says no such page")
	})
	// /hop/<remaining> redirects until it runs out, then serves the landing page.
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		var left int
		if _, err := fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/hop/"), "%d", &left); err != nil || left <= 0 {
			fmt.Fprint(w, "redirect-landing-marker")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", left-1), http.StatusFound)
	})
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.hits.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *webFixture) url(path string) string { return f.server.URL + path }

// TestW01_WebFetch is the oracle case for the native web_fetch tool (issue
// #358). Everything is served by a local fixture: no real network is touched.
func TestW01_WebFetch(t *testing.T) {
	fixture := newWebFixture(t)

	t.Run("denied without the web_network tier", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			managedToolStep("w01-denied", "web_fetch", map[string]any{"url": fixture.url("/page.html")}),
			providertest.ReplyStep("denied"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		before := fixture.hits.Load()
		result := runExec(t, home, provider.URL(), "fetch the page")
		if result.Err != nil {
			t.Fatalf("exec failed: %v %s", result.Err, result.Stderr)
		}
		if kinds := parseLedger(t, result.Stdout).failures(); len(kinds) != 1 || kinds[0].Tool != "web_fetch" || kinds[0].Kind != "denied" {
			t.Fatalf("default exec mode must deny web_fetch, got %#v", kinds)
		}
		if got := fixture.hits.Load() - before; got != 0 {
			t.Fatalf("denied call still issued %d request(s)", got)
		}
		next := provider.Snapshot().CompletionRequests[1]
		if strings.Contains(fmt.Sprint(next.Tools), "web_fetch") || !strings.Contains(requestText(next), "max_bytes") {
			t.Fatal("enable must return schema without changing native array")
		}
	})

	t.Run("granted web_network fetches, truncates, and flags failures", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			providertest.NativeToolCallStep("w01-search", "tools", map[string]any{"action": "search", "query": "web_fetch", "include_schema": true}),
			managedToolStep("w01-page", "web_fetch", map[string]any{"url": fixture.url("/page.html")}),
			managedToolStep("w01-big", "web_fetch", map[string]any{"url": fixture.url("/big.html"), "max_bytes": 2000}),
			managedToolStep("w01-404", "web_fetch", map[string]any{"url": fixture.url("/missing")}),
			managedToolStep("w01-hops", "web_fetch", map[string]any{"url": fixture.url("/hop/5")}),
			managedToolStep("w01-toomany", "web_fetch", map[string]any{"url": fixture.url("/hop/9")}),
			providertest.ReplyStep("fetched"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		env := map[string]string{"INVOKR_WEB_FETCH_ALLOW_PRIVATE_HOSTS": "1"}
		result := runExecEnv(t, home, provider.URL(), "fetch the fixture pages", env, "--allow", "web_network")
		if result.Err != nil {
			t.Fatalf("exec failed: %v %s", result.Err, result.Stderr)
		}
		requests := provider.Snapshot().CompletionRequests
		for _, want := range []string{
			"# Fixture Page", // HTML came back as readable text
			"codename 'amber-orbit-page'",
			"[truncated: response exceeded 2000 bytes;", // explicit cap marker
			"HTTP 404", // non-2xx detail reached the model
			"fixture says no such page",
			"redirect-landing-marker",   // a 5-hop chain is followed
			"stopped after 5 redirects", // a longer one errors
		} {
			if !requestsContain(requests, want) {
				t.Fatalf("model never saw %q", want)
			}
		}
		// encoding/json escapes markup, so check the escaped form of the tags too.
		for _, unwanted := range []string{"<h1>", `\u003ch1`, "leak()", "b{}"} {
			if requestsContain(requests, unwanted) {
				t.Fatalf("raw markup %q leaked into the model transcript", unwanted)
			}
		}
	})

	t.Run("private and link-local hosts are refused before dialing", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			managedToolStep("w01-linklocal", "web_fetch", map[string]any{"url": "http://169.254.169.254/latest/meta-data"}),
			managedToolStep("w01-loopback", "web_fetch", map[string]any{"url": fixture.url("/page.html")}),
			providertest.ReplyStep("blocked"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		before := fixture.hits.Load()
		result := runExec(t, home, provider.URL(), "fetch the metadata endpoint", "--allow", "web_network")
		if result.Err != nil {
			t.Fatalf("exec failed: %v %s", result.Err, result.Stderr)
		}
		requests := provider.Snapshot().CompletionRequests
		for _, want := range []string{"169.254.169.254", "refused to fetch private or loopback host"} {
			if !requestsContain(requests, want) {
				t.Fatalf("model never saw the refusal detail %q", want)
			}
		}
		if got := fixture.hits.Load() - before; got != 0 {
			t.Fatalf("blocked loopback fetch still issued %d request(s)", got)
		}
	})
}
