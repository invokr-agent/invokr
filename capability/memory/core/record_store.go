package core

import (
	"context"
	"time"
)

type RecordStore interface {
	Save(ctx context.Context, rec Record) (Record, error)
	Update(ctx context.Context, id string, rec Record) (Record, error)
	List(ctx context.Context, filter Query) ([]Record, error)
	Delete(ctx context.Context, id string) error
}

type LexicalStore interface {
	RecallFTS(ctx context.Context, req Query) ([]Hit, error)
}

type VectorStore interface {
	SaveVector(ctx context.Context, recordID string, vector []float32) error
	RecallVector(ctx context.Context, vector []float32, req Query) ([]Hit, error)
}

type MemoryEventRecord struct {
	ID        string            `json:"id,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	Role      string            `json:"role,omitempty"`
	Content   string            `json:"content,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type EventStore interface {
	AppendMemoryEvent(ctx context.Context, ev MemoryEventRecord) (MemoryEventRecord, error)
	RecentMemoryEvents(ctx context.Context, sessionID string, limit int) ([]MemoryEventRecord, error)
}
