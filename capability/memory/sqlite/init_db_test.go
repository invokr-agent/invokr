package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitDbAtPath_CreatesDatabaseAtPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")

	db, err := InitDbAtPath(dbPath)
	if err != nil {
		t.Fatalf("InitDbAtPath() error = %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	if err := ensureRequiredTables(db); err != nil {
		t.Fatalf("ensureRequiredTables() error = %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("os.Stat(%q) error = %v", dbPath, err)
	}
}

func TestInitDbAtPath_AppliesMigrationsAndCreatesRequiredTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "init-at-path.db")

	db, err := InitDbAtPath(dbPath)
	if err != nil {
		t.Fatalf("InitDbAtPath() error = %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	if err := ensureRequiredTables(db); err != nil {
		t.Fatalf("ensureRequiredTables() error = %v", err)
	}
}

func TestInitDbAtPath_MemoryDatabaseNeedsNoLockFile(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := InitDbAtPath(":memory:")
	if err != nil {
		t.Fatalf("InitDbAtPath() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := os.Stat(":memory:.migrate.lock"); !os.IsNotExist(err) {
		t.Fatalf("in-memory database created migration lock file: %v", err)
	}
}

func TestEnsureRequiredTables_ReturnsMissingTableError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing-tables.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	if _, err := db.Exec(`CREATE TABLE memory_records (id INTEGER PRIMARY KEY, kind TEXT, content TEXT, created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create memories table error = %v", err)
	}

	err = ensureRequiredTables(db)
	if err == nil {
		t.Fatalf("ensureRequiredTables() expected error, got nil")
	}
	if !strings.Contains(err.Error(), `required table "memory_records_fts" does not exist`) {
		t.Fatalf("ensureRequiredTables() error = %v, want missing memory_records_fts table error", err)
	}
}

// R09 regression: a corrupted database file must fail at open instead of
// silently returning fabricated rows from damaged pages later.
func TestInitDbAtPath_RejectsCorruptedDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "corrupt.db")

	db, err := InitDbAtPath(dbPath)
	if err != nil {
		t.Fatalf("InitDbAtPath() error = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memory_records (uuid, scope, kind, content, source, content_hash, created_at, updated_at) VALUES ('u1', 'global', 'note', 'seed', 'test', 'h1', '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatalf("seed insert error = %v", err)
	}
	_ = db.Close()

	// Flip bytes across the whole second half of the file so at least one
	// b-tree page is damaged regardless of how schema growth shifts the layout
	// (a single 64-byte flip can land in unused page interior and pass
	// quick_check).
	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db file error = %v", err)
	}
	for i := len(data) / 2; i < len(data); i++ {
		data[i] ^= 0xFF
	}
	if err := os.WriteFile(dbPath, data, 0o600); err != nil {
		t.Fatalf("write corrupted db error = %v", err)
	}

	if _, err := InitDbAtPath(dbPath); err == nil {
		t.Fatal("InitDbAtPath() expected error on corrupted database, got nil")
	}
}
