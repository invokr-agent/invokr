package sqlite

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/invokr-agent/invokr/support/migrate"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func InitDbAtPath(dbPath string) (*sql.DB, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			return nil, fmt.Errorf("sqlite: create db dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Every failure past this point has to close the half-open handle first.
	fail := func(err error) (*sql.DB, error) {
		_ = db.Close()
		return nil, err
	}

	// busy_timeout makes concurrent processes wait for each other's write
	// locks (notably the first-boot goose migration) instead of failing
	// immediately with SQLITE_BUSY. Set via PRAGMA so it applies on all
	// platforms (a file: DSN query string is dropped for Windows paths). It
	// touches no data pages, so it stays clean even on a corrupt file.
	if _, err := db.Exec("PRAGMA busy_timeout = 10000"); err != nil {
		return fail(fmt.Errorf("sqlite: set busy_timeout: %w", err))
	}

	// A damaged SQLite file can silently return fabricated rows from corrupted
	// pages; fail fast here instead of serving garbage as memory. quick_check
	// is also the first read that touches the pages, so it doubles as the
	// connection probe (a bare db.Ping would surface corruption as an
	// unwrapped driver error before this gate could name it).
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil {
		return fail(fmt.Errorf("sqlite: %s failed integrity check: %w", dbPath, err))
	}
	if check != "ok" {
		return fail(fmt.Errorf("sqlite: %s failed integrity check: %s", dbPath, check))
	}

	if dbPath != ":memory:" {
		unlock, err := lockMigrationFile(dbPath + ".migrate.lock")
		if err != nil {
			return fail(fmt.Errorf("sqlite: migration lock: %w", err))
		}
		defer unlock()
	}

	// WAL lets readers proceed while another process writes. memory.db is
	// user-global and the retrieval doc mirror (ADR 0015) makes concurrent
	// invokr instances write to it heavily; under the default rollback journal
	// a reader blocks on the writer's EXCLUSIVE lock and can outlast
	// busy_timeout. It rewrites the file header, so it must stay *after* the
	// integrity gate above: on a damaged file it fails first and would hide
	// the corruption diagnostic behind a bare driver error.
	//
	// The conversion also needs a momentary exclusive lock that SQLite
	// refuses to wait for — busy_timeout does not cover it — so two cold
	// starts racing on a fresh file made one of them fail with SQLITE_BUSY.
	// Running it under the init lock above serializes that; the second
	// process then finds the file already in WAL and the pragma is a no-op.
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return fail(fmt.Errorf("sqlite: set journal_mode: %w", err))
	}
	if err := migrate.RunMigrations(db, migrationsFS); err != nil {
		return fail(fmt.Errorf("sqlite: %w", err))
	}
	if err := ensureRequiredTables(db); err != nil {
		return fail(err)
	}
	return db, nil
}

func ensureRequiredTables(db *sql.DB) error {
	requiredTables := []string{"memory_records", "memory_records_fts", "memory_vectors",
		"raw_memory_events", "prompt_history", "jobs"}
	for _, table := range requiredTables {
		var name string
		err := db.QueryRow(`
			SELECT name
			FROM sqlite_master
			WHERE type = 'table' AND name = ?
		`, table).Scan(&name)
		if err == sql.ErrNoRows {
			return fmt.Errorf("sqlite: required table %q does not exist", table)
		}
		if err != nil {
			return fmt.Errorf("sqlite: failed checking table %q: %w", table, err)
		}
	}
	return nil
}
