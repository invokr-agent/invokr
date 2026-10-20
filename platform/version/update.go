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

const (
	defaultReleaseURL = "https://api.github.com/repos/invokr-ai/invokr/releases/latest"
	fetchTimeout      = 10 * time.Second
)

var (
	releaseURL  = defaultReleaseURL
	fetchClient = http.DefaultClient

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

	_, err := fetchOnce(ctx)

	fetchMu.Lock()
	result := latestVersion
	fetchMu.Unlock()
	return result, err
}

func fetchOnce(ctx context.Context) (bool, error) {
	fetchMu.Lock()
	if fetching {
		fetchMu.Unlock()
		return false, nil
	}
	fetching = true
	fetchMu.Unlock()

	tag, err := fetchLatestVersion(ctx)

	fetchMu.Lock()
	defer fetchMu.Unlock()
	fetching = false
	if err == nil {
		latestVersion = tag
		hasUpdate = semver.Compare(tag, Version) > 0
	}
	return true, err
}

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
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("version: close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("version: %s returned %s", releaseURL, resp.Status)
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

func refreshOnce(ctx context.Context) bool {
	ran, _ := fetchOnce(ctx)
	return ran
}
