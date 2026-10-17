package web_search

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/fsutil"
)

// cacheStore keeps full responses in the configured directory, including when
// relocated outside the workspace; relocation does not change privacy behavior.
type cacheStore struct {
	dir string
	now func() time.Time
}

type cacheEntry struct {
	CreatedAt time.Time    `json:"created_at"`
	Response  toolResponse `json:"response"`
}

func newCache(dir string, now func() time.Time) cacheStore {
	if now == nil {
		now = time.Now
	}
	return cacheStore{dir: strings.TrimSpace(dir), now: now}
}

func (c cacheStore) Read(args toolArgs) (toolResponse, bool) {
	var empty toolResponse
	if c.dir == "" {
		return empty, false
	}
	entry, existed, err := fsutil.ReadJSON[cacheEntry](c.path(args))
	if err != nil || !existed {
		return empty, false
	}
	if c.now().Sub(entry.CreatedAt) > cacheTTL(args.RecencyDays) {
		return empty, false
	}
	return entry.Response, true
}

func (c cacheStore) Write(args toolArgs, resp toolResponse) error {
	if c.dir == "" {
		return nil
	}
	return fsutil.SaveJSON(c.path(args), cacheEntry{CreatedAt: c.now(), Response: resp})
}

func (c cacheStore) path(args toolArgs) string { return filepath.Join(c.dir, cacheKey(args)+".json") }

func cacheKey(args toolArgs) string {
	parts := []string{strings.TrimSpace(args.Query), strings.Join(args.Providers, ","), strings.Join(args.Domains, ","), strconv.Itoa(args.RecencyDays), strconv.Itoa(args.MaxResults), "workspace_full", constants.WebSearchRankingVersion}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func cacheTTL(recencyDays int) time.Duration {
	switch {
	case recencyDays <= 1:
		return 15 * time.Minute
	case recencyDays <= 7:
		return time.Hour
	default:
		return 24 * time.Hour
	}
}
