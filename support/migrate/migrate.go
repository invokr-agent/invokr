// Package migrate centralizes goose SQL migrations behind a process-wide mutex.
// goose configures global dialect and base-FS state, so every migration run
// across the process must be serialized through RunMigrations.
package migrate

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sync"

	"github.com/pressly/goose/v3"
)

// gooseMu serializes access to goose's global dialect/base-FS state so
// concurrent callers from different packages cannot race.
var gooseMu sync.Mutex

// RunMigrations applies all pending "migrations" from fsys against db using
// the sqlite3 dialect. It is safe for concurrent use.
func RunMigrations(db *sql.DB, fsys fs.FS) error {
	gooseMu.Lock()
	defer gooseMu.Unlock()

	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(fsys)
	defer goose.SetBaseFS(nil)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
