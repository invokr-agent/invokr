package memory

import (
	"context"
	"fmt"

	"github.com/invokr-ai/invokr/capability/memory/core"
)

// vectorSource adapts an embedder + vector store into the recall.LexicalSource
// shape used for Tier 2 semantic recall.
type vectorSource struct {
	embedder interface {
		EmbedQueries(context.Context, []string) ([][]float32, error)
	}
	store core.VectorStore
}

func (s vectorSource) Recall(ctx context.Context, req core.Query) ([]core.Hit, error) {
	vectors, err := s.embedder.EmbedQueries(ctx, []string{req.Query})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("expected one query vector, got %d", len(vectors))
	}
	return s.store.RecallVector(ctx, vectors[0], req)
}
