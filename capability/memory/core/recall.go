package core

import "github.com/invokr-ai/invokr/config"

type Hit struct {
	Record     Record  `json:"record"`
	Score      float64 `json:"score,omitempty"`
	Provenance string  `json:"provenance,omitempty"`
}
type Query struct {
	Query     string               `json:"query,omitempty"`
	SessionID string               `json:"session_id,omitempty"`
	Scopes    []config.MemoryScope `json:"scopes,omitempty"`
	Kinds     []Kind               `json:"kinds,omitempty"`
	// Metadata restricts results to records whose metadata_json holds every
	// listed key/value pair. Doc records use it to scope a query to one
	// workspace (ADR 0015); the store ignores keys it cannot address safely.
	Metadata map[string]string `json:"metadata,omitempty"`
	Limit    int               `json:"limit,omitempty"`
}
