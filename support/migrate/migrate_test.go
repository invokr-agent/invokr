package migrate

import (
	"database/sql"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

func TestRunMigrations(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/00001_init.sql": &fstest.MapFile{Data: []byte(
			"-- +goose Up\nCREATE TABLE widgets (id INTEGER PRIMARY KEY);\n-- +goose Down\nDROP TABLE widgets;\n",
		)},
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := RunMigrations(db, fsys); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// The migration must have created the table.
	if _, err := db.Exec("INSERT INTO widgets (id) VALUES (1)"); err != nil {
		t.Fatalf("insert into migrated table: %v", err)
	}

	// Running again is idempotent (no pending migrations).
	if err := RunMigrations(db, fsys); err != nil {
		t.Fatalf("second RunMigrations: %v", err)
	}
}
