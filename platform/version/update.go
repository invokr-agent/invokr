package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// releaseURL is the GitHub Releases endpoint consulted for the latest published
// tag. It is a var (not a const) so tests can point it at an httptest server.
const defaultReleaseURL = "https://api.github.com/repos/invokr-ai/invokr/releases/latest"

var releaseURL = defaultReleaseURL

// fetchTimeout bounds a single upstream lookup.
const fetchTimeout = 10 * time.Second

// backgroundRefreshInterval is how often refresh() re-reads upstream while the
// process runs. It is intentionally long so a background cycle that is asleep
// between tests never wakes and touches shared state during the next test.
const backgroundRefreshInterval = time.Hour

// fetchClient is the HTTP client used for lookups; overridable in tests.
var fetchClient = http.DefaultClient

var (
	// latestVersion is the most recent release tag fetched from upstream, or
	// "" if no successful lookup has happened yet.
	latestVersion string

	// hasUpdate reports whether the most recent upstream lookup found a tag
	// newer than Version. It is recomputed inside each fetch cycle (while
	// fetching == true) and guarded by fetchMu.
	hasUpdate bool

	// fetching is true while a lookup is in flight. It gates concurrent
	// callers so at most one HTTP request is outstanding at a time. It is
	// guarded by fetchMu; callers must hold fetchMu to read or write it.
	fetching bool

	fetchMu sync.Mutex
)

// releaseResponse mirrors the subset of the GitHub "latest release" JSON that
// we care about.
type releaseResponse struct {
	TagName string `json:"tag_name"`
}

// LatestVersion returns the most recent published release tag for invokr,
// fetching it from upstream on first use and thereafter serving the cached
// value until the background refresh cycle updates it. Concurrent callers
// share a single in-flight request (single-flight).
//
// If the upstream lookup fails, LatestVersion returns an error and leaves any
// previously cached value in place.
func LatestVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	fetchMu.Lock()
	if fetching {
		// Another caller is mid-flight; return the latest cached result it
		// published (or "" if it has not completed yet) and let it finish.
		result := latestVersion
		fetchMu.Unlock()
		return result, nil
	}
	fetching = true
	fetchMu.Unlock()

	// Ensure any concurrent caller that waited on us gets the result and that
	// the flag is cleared exactly once, even on the error path.
	defer func() {
		fetchMu.Lock()
		fetching = false
		fetchMu.Unlock()
	}()

	tag, err := fetchLatestVersion(ctx)

	fetchMu.Lock()
	if err == nil {
		latestVersion = tag
	}
	fetchMu.Unlock()

	return latestVersion, err
}

// fetchLatestVersion performs the HTTP lookup and parses the tag. It must be
// called with the single-flight guard already acquired by LatestVersion.
func fetchLatestVersion(ctx context.Context) (string, error) {
	url := releaseURL
	client := fetchClient
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("version: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("version: lookup %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version: %s returned %s", url, resp.Status)
	}

	var rel releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("version: decode release: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("version: %s returned an empty tag_name", releaseURL)
	}
	return rel.TagName, nil
}

// UpdateAvailable reports whether a newer release tag exists than the one this
// binary was built with (Version). It reflects the most recent upstream lookup
// and returns false when no successful lookup has happened yet.
//
// hasUpdate is computed inside refreshOnce while fetching == true, so the only
// place Version is read by background code is the fetch cycle. The t.Cleanup
// that waits for fetching to go idle therefore establishes a happens-before
// edge to that read, preventing refresh() from reading Version after a test
// writes it.
func UpdateAvailable() bool {
	fetchMu.Lock()
	defer fetchMu.Unlock()
	return hasUpdate
}

// refresh periodically re-reads upstream so that the cached latestVersion and
// hasUpdate stay current for the life of the process. It is meant to be started
// once (e.g. from an application bootstrap) and run for the process lifetime.
// It blocks between cycles and never exits on its own.
//
// Each cycle reads Version only while the single-flight guard is held
// (fetching == true), so any observer that waits for fetching to go idle is
// guaranteed the read has completed before it mutates Version.
func refresh() {
	ticker := time.NewTicker(backgroundRefreshInterval)
	defer ticker.Stop()

	// Seed immediately, then on each tick.
	for {
		refreshOnce(context.Background())
		<-ticker.C
	}
}

// refreshOnce performs one fetch cycle: it fetches the latest tag and, while
// the single-flight guard is held, derives hasUpdate by reading Version. It
// returns whether a cycle ran.
func refreshOnce(ctx context.Context) bool {
	fetchMu.Lock()
	if fetching {
		fetchMu.Unlock()
		return false
	}
	fetching = true
	fetchMu.Unlock()

	defer func() {
		fetchMu.Lock()
		fetching = false
		fetchMu.Unlock()
	}()

	tag, err := fetchLatestVersion(ctx)
	if err != nil {
		return true
	}
	fetchMu.Lock()
	latestVersion = tag
	hasUpdate = semver.Compare(tag, Version) > 0
	fetchMu.Unlock()
	return true
}
