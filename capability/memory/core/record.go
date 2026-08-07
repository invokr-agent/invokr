package core

import (
	"strings"
	"time"

	"github.com/invokr-ai/invokr/config"
)

type Kind string

const (
	KindProfile    Kind = "profile"
	KindPreference Kind = "preference"
	KindFact       Kind = "fact"
	KindDecision   Kind = "decision"
	KindEpisode    Kind = "episode"
	KindSummary    Kind = "summary"
	// KindDoc is a workspace document mirrored into the store by the retrieval
	// tool (ADR 0015). Doc records only surface when a query names the kind
	// explicitly; default recall/list paths exclude them.
	KindDoc Kind = "doc"
)

const (
	// MetadataKeyPath is the metadata key holding a record's originating
	// workspace-relative path. Doc records set it.
	MetadataKeyPath = "path"
	// MetadataKeyWorkspace is the metadata key holding the absolute workspace
	// root a doc record was indexed from. One user-global memory.db serves
	// every workspace, so this is the discriminator that keeps repositories
	// from seeing, pruning, or overwriting each other's doc records.
	MetadataKeyWorkspace = "workspace"
)

// DedupeIdentity returns the text a record's content_hash is computed over,
// which the store's unique (scope, kind, content_hash) index dedupes on. Most
// kinds dedupe on content alone: the same fact saved twice is one record. A
// doc record mirrors exactly one file in one workspace, so its identity is
// (workspace, path, content): two files with identical text stay two records,
// editing one to match another updates it in place instead of colliding, and
// the same relative path in two repositories stays two records.
func DedupeIdentity(rec Record) string {
	if rec.Kind == KindDoc {
		return strings.Join([]string{
			rec.Metadata[MetadataKeyWorkspace], rec.Metadata[MetadataKeyPath], rec.Content}, "\x00")
	}
	return rec.Content
}

type Source string

const (
	SourceMarkdown Source = "markdown"
	SourceSQLite   Source = "sqlite"
	SourceRuntime  Source = "runtime"
)

type Record struct {
	ID        string             `json:"id,omitempty"`
	Scope     config.MemoryScope `json:"scope,omitempty"`
	Kind      Kind               `json:"kind,omitempty"`
	Content   string             `json:"content,omitempty"`
	Summary   string             `json:"summary,omitempty"`
	Source    Source             `json:"source,omitempty"`
	SessionID string             `json:"session_id,omitempty"`
	// Metadata carries small structured facts about the record's origin (e.g.
	// a doc record's workspace path, file size, mtime, and content hash). It is
	// persisted as JSON in the sqlite store's metadata_json column.
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
}
