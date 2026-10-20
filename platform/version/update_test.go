package version

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pristineVersion is the build identity as the package came in, captured on the
// first injected endpoint so a test that stamps it can put it back afterwards.
var pristineVersion = Version
var restoreOnce sync.Once

// okTag answers with a release object carrying tag.
func okTag(tag string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(releaseResponse{TagName: tag})
	}
}

// answerWith answers with status and body.
func answerWith(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); _, _ = w.Write([]byte(body)) }
}

// setVersion writes the build identity under the fetch lock, which is how the
// checker reads it.
func setVersion(v string) {
	fetchMu.Lock()
	Version = v
	fetchMu.Unlock()
}

// resetCache clears the fetched answer so a test starts from a clean slate.
func resetCache() {
	fetchMu.Lock()
	newestTag = ""
	checkedAt = time.Time{}
	fetching = false
	fetchMu.Unlock()
}

// stubRelease points the lookup at a local server, counts the requests it
// serves, and restores the injected fields when the test ends. The returned
// function reports the request count.
func stubRelease(t *testing.T, respond func(http.ResponseWriter, *http.Request)) func() int {
	var seen int32
	restoreOnce.Do(func() { pristineVersion = Version })
	origURL, origClient := releaseURL, fetchClient
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = atomic.AddInt32(&seen, 1); respond(w, r) }))
	releaseURL, fetchClient = srv.URL, srv.Client()
	t.Cleanup(func() {
		waitForFetchingIdle(t)
		srv.Close()
		releaseURL, fetchClient = origURL, origClient
		setVersion(pristineVersion)
	})
	return func() int { return int(atomic.LoadInt32(&seen)) }
}

// waitForFetchingIdle blocks until no fetch is in flight. claimFetch sets
// fetching when it starts one and runFetch clears it under the same lock, after
// the fetch has read releaseURL and fetchClient, so an idle observation is
// ordered after those reads: restoring them cannot race with a running fetch.
func waitForFetchingIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		fetchMu.Lock()
		idle := !fetching
		fetchMu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a background fetch to finish")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTagOrdering compares the fetched tag against the build identity: only a
// newer tag is returned, and the comparison is semantic, not lexical.
func TestTagOrdering(t *testing.T) {
	cases := []struct {
		latest, build, want string
	}{
		{"v0.3.0", "v0.2.0", "v0.3.0"},
		{"v0.2.0", "v0.2.0", ""},
		{"v0.1.0", "v0.2.0", ""},
		{"v0.10.0", "v0.9.0", "v0.10.0"},
	}
	for _, c := range cases {
		resetCache()
		count := stubRelease(t, okTag(c.latest))
		setVersion(c.build)
		refreshOnce(context.Background())
		got := LatestVersion()
		if got != c.want {
			t.Fatalf("latest %s vs build %s: got %q, want %q", c.latest, c.build, got, c.want)
		}
		if n := count(); n != 1 {
			t.Fatalf("requests = %d, want 1", n)
		}
	}
}

// TestDevBuildMakesNoRequest: an unstamped build has no release to compare
// against, so the check stays off and no request is made.
func TestDevBuildMakesNoRequest(t *testing.T) {
	setVersion("dev")
	resetCache()
	count := stubRelease(t, okTag("v0.3.0"))
	if got := LatestVersion(); got != "" {
		t.Fatalf("LatestVersion = %q, want \"\" for an unstamped build", got)
	}
	if n := count(); n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
}

// TestEnvOptOutMakesNoRequest: INVOKR_NO_UPDATE_CHECK turns the check off.
func TestEnvOptOutMakesNoRequest(t *testing.T) {
	_ = os.Setenv(noUpdateCheckEnv, "1")
	t.Cleanup(func() { _ = os.Unsetenv(noUpdateCheckEnv) })
	resetCache()
	count := stubRelease(t, okTag("v0.3.0"))
	setVersion("v0.1.0")
	if got := LatestVersion(); got != "" {
		t.Fatalf("LatestVersion = %q, want \"\" with the opt-out set", got)
	}
	if n := count(); n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
}

// TestBadResponsesKeepPreviousValue: a failed lookup keeps the previous answer.
// The endpoint has no empty-list shape, so the no-release cases are a non-200
// status and a response with no tag_name.
func TestBadResponsesKeepPreviousValue(t *testing.T) {
	resetCache()
	count := stubRelease(t, okTag("v0.3.0"))
	setVersion("v0.2.0")
	refreshOnce(context.Background())
	if got := LatestVersion(); got != "v0.3.0" {
		t.Fatalf("seeded answer = %q, want %q", got, "v0.3.0")
	}
	_ = stubRelease(t, answerWith(http.StatusInternalServerError, ""))
	refreshOnce(context.Background())
	if got := LatestVersion(); got != "v0.3.0" {
		t.Fatalf("after a non-200: answer = %q, want the previous %q", got, "v0.3.0")
	}
	_ = stubRelease(t, answerWith(http.StatusOK, "not json"))
	refreshOnce(context.Background())
	if got := LatestVersion(); got != "v0.3.0" {
		t.Fatalf("after bad JSON: answer = %q, want the previous %q", got, "v0.3.0")
	}
	_ = stubRelease(t, answerWith(http.StatusOK, "{}"))
	refreshOnce(context.Background())
	if got := LatestVersion(); got != "v0.3.0" {
		t.Fatalf("after no tag_name: answer = %q, want the previous %q", got, "v0.3.0")
	}
	if n := count(); n != 1 {
		t.Fatalf("requests = %d, want 1 (only the seeding lookup)", n)
	}
}

// TestTTLScheduling: the first call fetches in the background, a second call
// inside the TTL makes no request, and a call whose cache is older than the
// TTL makes one.
func TestTTLScheduling(t *testing.T) {
	resetCache()
	count := stubRelease(t, okTag("v0.2.0"))
	setVersion("v0.1.0")
	LatestVersion()
	waitForFetchingIdle(t)
	if n := count(); n != 1 {
		t.Fatalf("first call: requests = %d, want 1", n)
	}
	LatestVersion()
	if n := count(); n != 1 {
		t.Fatalf("second call inside the TTL: requests = %d, want 1", n)
	}
	fetchMu.Lock()
	checkedAt = time.Now().Add(-cacheTTL - time.Hour)
	fetchMu.Unlock()
	LatestVersion()
	waitForFetchingIdle(t)
	if n := count(); n != 2 {
		t.Fatalf("call after the TTL: requests = %d, want 2", n)
	}
}

// TestConcurrentStartsOneFetch: concurrent callers claim one fetch at most.
func TestConcurrentStartsOneFetch(t *testing.T) {
	setVersion("v0.1.0")
	resetCache()
	count := stubRelease(t, okTag("v0.2.0"))
	var wg sync.WaitGroup
	wg.Add(20)
	for i := 0; i < 20; i++ {
		go func() { LatestVersion(); wg.Done() }()
	}
	wg.Wait()
	waitForFetchingIdle(t)
	if n := count(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}
