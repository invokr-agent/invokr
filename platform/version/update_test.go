package version

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLatestVersionConcurrentStartsOneFetch(t *testing.T) {
	var requests int32
	release := make(chan struct{})
	started := make(chan struct{})
	opened := make(chan struct{}) // signals a second request reached the server

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if n == 1 {
			close(started)
		} else {
			select {
			case <-opened:
			default:
				close(opened)
			}
		}
		<-release // hold the first request open so concurrent callers observe fetching
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(releaseResponse{TagName: "v9.9.9"})
	}))
	defer srv.Close()

	origURL, origClient := releaseURL, fetchClient
	releaseURL = srv.URL
	fetchClient = srv.Client()
	defer func() { releaseURL, fetchClient = origURL, origClient }()

	// Clear leftover state from a prior iteration's background refresh.
	fetchMu.Lock()
	fetching = false
	latestVersion = ""
	hasUpdate = false
	fetchMu.Unlock()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = LatestVersion(context.Background())
		}()
	}

	// Wait until the first request lands; if a second opens before release,
	// the single-flight guard failed.
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the first fetch to start")
	}
	select {
	case <-opened:
		t.Fatal("a second concurrent request reached the server; single-flight guard failed")
	case <-time.After(200 * time.Millisecond):
	}

	close(release) // let the in-flight fetch (and the waiters) complete
	wg.Wait()

	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// TestUpdateAvailableFix covers the race where a background refresh() goroutine
// reads Version during its fetch cycle. The cleanup blocks until no fetch is in
// flight, so refresh() cannot touch Version after a test writes it.
func TestUpdateAvailableFix(t *testing.T) {
	origURL, origClient := releaseURL, fetchClient
	origVersion := Version
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(releaseResponse{TagName: "v2.0.0"})
	}))
	defer srv.Close()
	releaseURL = srv.URL
	fetchClient = srv.Client()
	defer func() {
		releaseURL, fetchClient = origURL, origClient
		Version = origVersion
	}()

	// Cleanup: block until no fetch is in flight so refresh cannot read Version
	// while the next test writes it.
	t.Cleanup(func() {
		waitForFetchingIdle(t)
	})

	// Seed cached state via the background refresh path.
	go func() { refreshOnce(context.Background()) }()
	waitForFetchingIdle(t)

	Version = "v1.0.0" // below the latest v2.0.0
	refreshOnce(context.Background())
	if !UpdateAvailable() {
		t.Fatal("UpdateAvailable() = false, want true after setting Version below latest")
	}

	Version = "v3.0.0" // above the latest v2.0.0
	refreshOnce(context.Background())
	if UpdateAvailable() {
		t.Fatal("UpdateAvailable() = true, want false when Version is at/above latest")
	}
}

// TestRefreshReadsVersionDuringFetch proves the race fix end to end: a background
// refresh cycle reads Version during its fetch cycle, and the cleanup blocks
// until fetching goes idle so the goroutine cannot read Version after the test
// writes it. Under -race it is clean because the idle wait observes the
// happens-before edge on fetching. The goroutine is a single bounded refreshOnce
// (not an unbounded refresh loop) so it completes on its own.
func TestRefreshReadsVersionDuringFetch(t *testing.T) {
	origURL, origClient := releaseURL, fetchClient
	origVersion := Version
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(releaseResponse{TagName: "v4.5.6"})
	}))
	defer srv.Close()
	releaseURL = srv.URL
	fetchClient = srv.Client()
	defer func() {
		releaseURL, fetchClient = origURL, origClient
		Version = origVersion
	}()

	// Cleanup: block until no fetch is in flight.
	t.Cleanup(func() { waitForFetchingIdle(t) })

	// Background refresh reads Version during its fetch cycle (fetching == true).
	go refreshOnce(context.Background())
	// Block until that read completes before the test writes Version again.
	waitForFetchingIdle(t)

	Version = "v4.5.6" // equal to latest -> no update available
	waitForFetchingIdle(t)
	if UpdateAvailable() {
		t.Fatal("UpdateAvailable() = true, want false when Version equals latest")
	}
}

// waitForFetchingIdle blocks until no fetch is in flight. refreshOnce reads
// Version only while fetching is true, so observing fetching == false
// establishes a happens-before edge to that read.
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
			t.Fatal("timed out waiting for fetch to become idle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFetchLatestVersionParsesTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(releaseResponse{TagName: "v3.1.4"})
	}))
	defer srv.Close()

	origURL, origClient := releaseURL, fetchClient
	releaseURL = srv.URL
	fetchClient = srv.Client()
	defer func() { releaseURL, fetchClient = origURL, origClient }()

	got, err := fetchLatestVersion(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestVersion: %v", err)
	}
	if got != "v3.1.4" {
		t.Fatalf("fetchLatestVersion = %q, want %q", got, "v3.1.4")
	}
}

func TestFetchLatestVersionErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	origURL, origClient := releaseURL, fetchClient
	releaseURL = srv.URL
	fetchClient = srv.Client()
	defer func() { releaseURL, fetchClient = origURL, origClient }()

	if _, err := fetchLatestVersion(context.Background()); err == nil {
		t.Fatal("fetchLatestVersion error = nil, want error on 500")
	}
}
