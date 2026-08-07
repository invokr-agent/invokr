package web_search

import (
	"cmp"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/tools/core"
)

func dedupeResults(results []ProviderResult) []SearchResult {
	merged := make([]SearchResult, 0, len(results))
	for _, result := range results {
		result = normalizeProviderResult(result)
		if result.URL == "" {
			continue
		}
		canonical := canonicalURL(result.URL)
		idx := findDuplicate(merged, result, canonical)
		if idx == -1 {
			merged = append(merged, SearchResult{Title: result.Title, URL: result.URL, Snippet: result.Snippet, Provider: result.Provider, Domain: result.Domain, PublishedAt: result.PublishedAt, RetrievedAt: result.RetrievedAt, ProvidersReturnedBy: []string{result.Provider}, CanonicalURL: canonical, ProviderScores: map[string]float64{result.Provider: result.Score}})
			continue
		}
		existing := &merged[idx]
		if result.Score > existing.ProviderScores[existing.Provider] {
			existing.Title = core.FirstNonEmptyTrimmed(result.Title, existing.Title)
			existing.URL = core.FirstNonEmptyTrimmed(result.URL, existing.URL)
			existing.Snippet = core.FirstNonEmptyTrimmed(result.Snippet, existing.Snippet)
			existing.Provider = result.Provider
			existing.Domain = core.FirstNonEmptyTrimmed(result.Domain, existing.Domain)
			existing.PublishedAt = newerTime(existing.PublishedAt, result.PublishedAt)
			existing.RetrievedAt = newerValueTime(existing.RetrievedAt, result.RetrievedAt)
			existing.CanonicalURL = core.FirstNonEmptyTrimmed(canonical, existing.CanonicalURL)
		}
		existing.ProviderScores[result.Provider] = math.Max(existing.ProviderScores[result.Provider], result.Score)
		if !slices.Contains(existing.ProvidersReturnedBy, result.Provider) {
			existing.ProvidersReturnedBy = append(existing.ProvidersReturnedBy, result.Provider)
		}
	}
	for i := range merged {
		slices.Sort(merged[i].ProvidersReturnedBy)
	}
	return merged
}

func rankResults(results []SearchResult, query string, now time.Time) []SearchResult {
	queryTerms := strings.Fields(strings.ToLower(query))
	for i := range results {
		results[i].FinalScore = finalScore(results[i], queryTerms, now)
	}
	slices.SortStableFunc(results, func(a, b SearchResult) int {
		return cmp.Or(cmp.Compare(b.FinalScore, a.FinalScore), cmp.Compare(a.CanonicalURL, b.CanonicalURL))
	})
	return results
}

func finalScore(result SearchResult, queryTerms []string, now time.Time) float64 {
	relevance := textRelevance(strings.ToLower(result.Title+" "+result.Snippet), queryTerms)
	confidence := bestProviderScore(result.ProviderScores)
	freshness := freshnessScore(result.PublishedAt, now)
	overlap := math.Min(float64(len(result.ProvidersReturnedBy))/3.0, 1)
	return mathutil.Clamp01(0.4*relevance + 0.25*freshness + 0.25*confidence + 0.1*overlap)
}

func findDuplicate(results []SearchResult, result ProviderResult, canonical string) int {
	title := normalizeTitle(result.Title)
	for i, existing := range results {
		if canonical != "" && existing.CanonicalURL == canonical {
			return i
		}
		if normalizedURL(existing.URL) == normalizedURL(result.URL) {
			return i
		}
		if existing.Domain == result.Domain && titleSimilarity(normalizeTitle(existing.Title), title) >= 0.8 {
			return i
		}
	}
	return -1
}

func canonicalURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(strings.TrimPrefix(u.Host, "www."))
	q := u.Query()
	for _, key := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "fbclid", "gclid"} {
		q.Del(key)
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String()
}

func normalizedURL(raw string) string {
	return strings.TrimPrefix(strings.TrimPrefix(canonicalURL(raw), "https://"), "http://")
}

func normalizeTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	replacer := strings.NewReplacer("-", " ", "_", " ", ":", " ", "|", " ")
	return strings.Join(strings.Fields(replacer.Replace(title)), " ")
}

func titleSimilarity(a, b string) float64 {
	aw, bw := core.StringSet(strings.Fields(a)), core.StringSet(strings.Fields(b))
	if len(aw) == 0 || len(bw) == 0 {
		return 0
	}
	shared := 0
	for word := range aw {
		if _, ok := bw[word]; ok {
			shared++
		}
	}
	return float64(shared) / float64(max(len(aw), len(bw)))
}

func textRelevance(text string, terms []string) float64 {
	if len(terms) == 0 {
		return 0.5
	}
	matches := 0
	for _, term := range terms {
		if strings.Contains(text, term) {
			matches++
		}
	}
	return float64(matches) / float64(len(terms))
}

func bestProviderScore(scores map[string]float64) float64 {
	best := 0.0
	for _, score := range scores {
		best = max(best, score)
	}
	return mathutil.Clamp01(best)
}

func freshnessScore(published *time.Time, now time.Time) float64 {
	if published == nil {
		return 0.25
	}
	age := now.Sub(*published)
	switch {
	case age <= 24*time.Hour:
		return 1
	case age <= 7*24*time.Hour:
		return 0.8
	case age <= 31*24*time.Hour:
		return 0.5
	default:
		return 0.2
	}
}

func newerTime(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.After(*b) {
		return a
	}
	return b
}

func newerValueTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
