package history

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/invokr-ai/invokr/support/strutil"
	"path/filepath"
	"strings"
	"sync"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/memory/sqlite"

	"github.com/stytchauth/sqx"
)

// DefaultLimit is how many prompts are kept per workspace root, not in total:
// a busy project must not evict every other project's history.
const DefaultLimit = 300

type Store struct {
	db    *sql.DB
	mu    sync.Mutex
	cwd   string
	limit int
}

type Config struct {
	Dir string
	// Cwd is the workspace root prompts are recorded under and recalled from.
	Cwd   string
	Limit int
}

type promptHistoryRow struct {
	Text      string `db:"text"`
	UpdatedAt string `db:"updated_at"`
	ID        int64  `db:"id"`
}

func NewStore(cfg Config) (*Store, error) {
	limit := cfg.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	// Prompt history lives in the shared memory.db (same precedent as
	// jobs.OpenStore): the migration set already creates prompt_history there,
	// so a separate history.db only duplicated the full schema and paid a
	// second integrity-check/migration pass at startup. A pre-existing
	// history.db on disk is left untouched and its rows are simply no longer
	// read.
	dbPath := filepath.Join(cfg.Dir, "memory.db")
	db, err := sqlite.InitDbAtPath(dbPath)
	if err != nil {
		// memory.db now has more than one opener, so the error must name this
		// one or it is indistinguishable from the memory manager's own failure
		// on the same file.
		return nil, fmt.Errorf("prompt history: open db: %w", err)
	}

	return &Store{db: db, cwd: cfg.Cwd, limit: limit}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Append(ctx context.Context, text string) error {
	if err := s.check(); err != nil {
		return err
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	now := core.UTCNow()
	// Hashing the pair rather than the text alone keeps the existing UNIQUE
	// constraint on content_hash meaningful once rows are cwd-scoped: the same
	// prompt typed in two directories is two independent rows.
	hash := strutil.HashHexTrimmed(s.cwd + "\x00" + text)

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("prompt history: begin append: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// sqx.Delete emits MySQL-style DELETE table FROM table, which SQLite rejects.
	if _, err := tx.ExecContext(ctx, `DELETE FROM prompt_history WHERE content_hash = ?`, hash); err != nil {
		return fmt.Errorf("prompt history: delete duplicate: %w", err)
	}

	if _, err := sqx.Write(ctx).WithQueryable(tx).Insert("prompt_history").
		Columns("text", "content_hash", "cwd", "created_at", "updated_at").Values(text, hash, s.cwd, now, now).
		DoResult(); err != nil {
		return fmt.Errorf("prompt history: append: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM prompt_history
		WHERE cwd = ?
		  AND id NOT IN (
			SELECT id
			FROM prompt_history
			WHERE cwd = ?
			ORDER BY datetime(updated_at) DESC, id DESC
			LIMIT ?
		)
	`, s.cwd, s.cwd, s.limit); err != nil {
		return fmt.Errorf("prompt history: prune: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("prompt history: commit append: %w", err)
	}
	return nil
}

func (s *Store) Recent(ctx context.Context, limit int) ([]string, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > s.limit {
		limit = s.limit
	}

	rows, err := sqx.Read[promptHistoryRow](ctx).WithQueryable(s.db).Select("text", "updated_at", "id").
		From("prompt_history").Where("cwd = ?", s.cwd).OrderBy("datetime(updated_at) DESC", "id DESC").
		Limit(uint64(limit)).All()
	if err != nil {
		return nil, fmt.Errorf("prompt history: recent: %w", err)
	}

	history := make([]string, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		history = append(history, rows[i].Text)
	}

	return history, nil
}

func (s *Store) check() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("prompt history: nil store")
	}
	return nil
}
