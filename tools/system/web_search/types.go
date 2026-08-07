package web_search

import (
	"context"
	"time"
)

type Provider interface {
	Name() string
	Search(context.Context, SearchRequest) ([]ProviderResult, error)
}

type SearchRequest struct {
	Query                   string
	Domains                 []string
	RecencyDays, MaxResults int
}

type ProviderResult struct {
	Title, URL, Snippet, Provider, Domain string
	PublishedAt                           *time.Time
	RetrievedAt                           time.Time
	Score                                 float64
}

type SearchResult struct {
	Title               string             `json:"title"`
	URL                 string             `json:"url"`
	Snippet             string             `json:"snippet"`
	Provider            string             `json:"provider"`
	Domain              string             `json:"domain"`
	PublishedAt         *time.Time         `json:"published_at,omitempty"`
	RetrievedAt         time.Time          `json:"retrieved_at"`
	Rank                int                `json:"rank"`
	ProvidersReturnedBy []string           `json:"providers_returned_by"`
	CanonicalURL        string             `json:"canonical_url"`
	DuplicateOf         *string            `json:"duplicate_of"`
	ProviderScores      map[string]float64 `json:"provider_scores"`
	FinalScore          float64            `json:"final_score"`
}
