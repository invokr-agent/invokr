package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

const (
	defaultReleaseURL = "https://api.github.com/repos/invokr-ai/invokr/releases/latest"
	fetchTimeout      = 10 * time.Second
	// cacheTTL is how long a fetched answer is reused before another lookup runs.
	// noUpdateCheckEnv turns the lookup off when set to any non-empty value.
	cacheTTL         = 24 * time.Hour
	noUpdateCheckEnv = "INVOKR_NO_UPDATE_CHECK"
)

var (
	releaseURL  = defaultReleaseURL
	fetchClient = http.DefaultClient

	// newestTag is the newest release tag a lookup has fetched, "" until one
	// succeeds. checkedAt is when the last lookup finished, success or failure
	// alike, so a failed lookup waits a TTL before trying again.
	newestTag string
	checkedAt time.Time
	fetching  bool

	fetchMu sync.Mutex
)

type releaseResponse struct {
	TagName string `json:"tag_name"`
}

// disabled reports whether this process checks for updates at all: an unstamped
// dev build has no release to compare against, and the env opt-out is explicit.
func disabled() bool {
	return Version == "dev" || os.Getenv(noUpdateCheckEnv) != ""
}

// claimFetch takes the fetch slot when a lookup should run: none is in flight,
// the check is not switched off, and the cache is older than the TTL unless the
// caller forces one. Claiming under the lock is what keeps concurrent callers
// down to a single request.
func claimFetch(force bool) bool {
	fetchMu.Lock()
	defer fetchMu.Unlock()
	if fetching || disabled() || (!force && checkedAt.Add(cacheTTL).After(time.Now())) {
		return false
	}
	fetching = true
	return true
}

// fetchLatestVersion asks the release endpoint for the newest release tag.
func fetchLatestVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return "", fmt.Errorf("version: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := fetchClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("version: lookup %s: %w", releaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version: %s returned %s", releaseURL, resp.Status)
	}

	var rel releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("version: decode release: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("version: %s returned no tag_name", releaseURL)
	}
	return rel.TagName, nil
}

// runFetch performs one lookup and records the outcome. checkedAt advances
// either way, so a failed check waits a TTL before retrying; only a successful
// lookup replaces the answer, which keeps the previous value on failure. The
// lookup error decides that replacement and is not surfaced: a failed check is
// silent to the user.
func runFetch(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	tag, err := fetchLatestVersion(ctx)

	fetchMu.Lock()
	defer fetchMu.Unlock()
	fetching = false
	checkedAt = time.Now()
	if err == nil {
		newestTag = tag
	}
}

// refreshOnce runs one lookup on the caller's thread, ignoring the TTL, so a
// test can read the cache the moment the call returns.
func refreshOnce(ctx context.Context) bool {
	if !claimFetch(true) {
		return false
	}
	runFetch(ctx)
	return true
}

// LatestVersion is the badge's answer: the newest tag a lookup fetched, and only
// when it is newer than this build. It never blocks. A stale cache starts a
// background fetch whose result a later render picks up.
func LatestVersion() string {
	if claimFetch(false) {
		// Off the render path: nothing here waits on the fetch, and a background
		// lookup has no caller to report to, so runFetch records the outcome.
		go func() { runFetch(context.Background()) }()
	}

	fetchMu.Lock()
	defer fetchMu.Unlock()
	if semver.Compare(newestTag, Version) > 0 {
		return newestTag
	}
	return ""
}
