package messaging

import (
	"context"
	"fmt"
	"sync"

	"github.com/invokr-agent/invokr/support/fsutil"
)

// Store maps chat keys to agent session IDs, persisted as a JSON file.
// A chat key is "<transport-name>:<chatID>", e.g. "telegram:123456789".
//
// The file is read once at construction and the map is the live copy; each
// write rewrites the whole file atomically, so the mutex serializes writers
// within a process. Across processes, `bot serve` is single-instance per
// memory dir: startup takes an exclusive flock on `<Memory.Dir>/bot-serve.lock`
// and a second instance refuses to start, so no two writers share this file.
type Store struct {
	mu       sync.Mutex
	path     string
	sessions map[string]string
}

// NewStore loads (or initializes) the JSON session map at path. A missing file
// is not an error; a corrupt file is, so a damaged map is surfaced instead of
// silently replaced.
func NewStore(path string) (*Store, error) {
	sessions, existed, err := fsutil.ReadJSON[map[string]string](path)
	if err != nil {
		if existed {
			return nil, fmt.Errorf("messaging store: parse %s: %w", path, err)
		}
		return nil, fmt.Errorf("messaging store: read %s: %w", path, err)
	}
	if sessions == nil {
		sessions = map[string]string{}
	}
	return &Store{path: path, sessions: sessions}, nil
}

// Close is a no-op, kept so callers of the previous database-backed store
// keep working unchanged.
func (s *Store) Close() error { return nil }

// SessionID returns the stored session ID for the chat key, or "" if none.
func (s *Store) SessionID(_ context.Context, chatKey string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[chatKey], nil
}

// SetSessionID upserts the session ID for the given chat key and persists the
// whole map. A failed write rolls back the in-memory entry so the store never
// reports a mapping that is not on disk.
func (s *Store) SetSessionID(_ context.Context, chatKey string, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, had := s.sessions[chatKey]
	s.sessions[chatKey] = sessionID
	if err := s.save(); err != nil {
		if had {
			s.sessions[chatKey] = previous
		} else {
			delete(s.sessions, chatKey)
		}
		return err
	}
	return nil
}

// save rewrites the map at 0600. The replace is atomic: SetSessionID rewrites
// every entry and NewStore fails closed on a parse error, so a torn write
// would cost the whole map and stop `bot serve` from starting.
// Callers must hold s.mu.
func (s *Store) save() error {
	if err := fsutil.SaveJSON(s.path, s.sessions); err != nil {
		return fmt.Errorf("messaging store: write %s: %w", s.path, err)
	}
	return nil
}
