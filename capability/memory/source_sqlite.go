package memory

import (
	"context"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/sqlite"
)

// sqliteLexicalSource adapts the sqlite store's full-text search into the
// recall.LexicalSource shape.
type sqliteLexicalSource struct {
	sqlite *sqlite.Memory
}

func (s sqliteLexicalSource) Recall(ctx context.Context, req core.Query) ([]core.Hit, error) {
	return s.sqlite.RecallFTS(ctx, req)
}
