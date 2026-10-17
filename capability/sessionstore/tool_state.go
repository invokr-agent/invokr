package sessionstore

import (
	"strings"

	"github.com/invokr-ai/invokr/support/fsutil"
)

// ToolState is session metadata, not model-authored transcript content.
type ToolState struct {
	Enabled []string      `json:"enabled"`
	Catalog []ToolListing `json:"catalog"`
}
type ToolListing struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Store) toolStatePath(id string) string {
	return strings.TrimSuffix(s.path(id), ".jsonl") + ".tools.json"
}
func (s *Store) LoadToolState(id string) (ToolState, error) {
	state, _, err := fsutil.ReadJSON[ToolState](s.toolStatePath(id))
	return state, err
}
func (s *Store) SaveToolState(id string, state ToolState) error {
	return fsutil.SaveJSON(s.toolStatePath(id), state)
}
