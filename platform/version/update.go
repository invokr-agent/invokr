package version

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

const defaultReleaseURL = "https://api.github.com/repos/invokr-ai/invokr/releases/latest"

var releaseURL = defaultReleaseURL

const fetchTimeout = 10 * time.Second

const backgroundRefreshInterval = time.Hour

var fetchClient = http.DefaultClient

var (
	latestVersion string
	hasUpdate     bool
	fetching      bool

	fetchMu sync.Mutex
)

type releaseResponse struct {
	TagName string `json:"tag_name"`
}

func LatestVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	fetchMu.Lock()
	if fetching {
		result := latestVersion
		fetchMu.Unlock()
		return result, nil
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
		return latestVersion, err
	}
	fetchMu.Lock()
	latestVersion = tag
	hasUpdate = semver.Compare(tag, Version) > 0
	fetchMu.Unlock()
	return latestVersion, nil
}

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
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("version: close response body: %v", err)
		}
	}()

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

func UpdateAvailable() bool {
	fetchMu.Lock()
	defer fetchMu.Unlock()
	return hasUpdate
}

// Start launches the background refresh loop that keeps hasUpdate fresh for
// UpdateAvailable(). Call once at startup.
func Start() {
	go refresh()
}

func refresh() {
	ticker := time.NewTicker(backgroundRefreshInterval)
	defer ticker.Stop()

	for {
		refreshOnce(context.Background())
		<-ticker.C
	}
}

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
