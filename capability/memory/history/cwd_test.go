package history

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// twoStores opens two prompt-history stores over the same database, each
// scoped to a different workspace root — the shape of two projects sharing one
// invokr home.
func twoStores(t *testing.T, limit int) (*Store, *Store) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "memory")
	open := func(cwd string) *Store {
		store, err := NewStore(Config{Dir: dir, Cwd: cwd, Limit: limit})
		if err != nil {
			t.Fatalf("NewStore(%s) error = %v", cwd, err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	return open("/work/alpha"), open("/work/beta")
}

func recent(t *testing.T, store *Store) []string {
	t.Helper()
	got, err := store.Recent(context.Background(), 0)
	if err != nil {
		t.Fatalf("Recent() error = %v", err)
	}
	return got
}

func TestRecentIsScopedToCwd(t *testing.T) {
	ctx := context.Background()
	alpha, beta := twoStores(t, DefaultLimit)

	if err := alpha.Append(ctx, "make verify"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := beta.Append(ctx, "npm test"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if got := recent(t, alpha); !reflect.DeepEqual(got, []string{"make verify"}) {
		t.Fatalf("alpha Recent() = %v, want [make verify]", got)
	}
	if got := recent(t, beta); !reflect.DeepEqual(got, []string{"npm test"}) {
		t.Fatalf("beta Recent() = %v, want [npm test]", got)
	}
}

// The same prompt in two directories must be two independent rows: the UNIQUE
// content_hash is computed over cwd+text precisely so one does not steal the
// other's row.
func TestSameTextInTwoDirectoriesKeepsBothRows(t *testing.T) {
	ctx := context.Background()
	alpha, beta := twoStores(t, DefaultLimit)

	if err := alpha.Append(ctx, "make verify"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := beta.Append(ctx, "make verify"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if got := recent(t, alpha); !reflect.DeepEqual(got, []string{"make verify"}) {
		t.Fatalf("alpha lost its row: Recent() = %v", got)
	}
	if got := recent(t, beta); !reflect.DeepEqual(got, []string{"make verify"}) {
		t.Fatalf("beta Recent() = %v", got)
	}
}

// The limit is per directory, so a busy project must not evict another's history.
func TestPruneIsPerDirectory(t *testing.T) {
	ctx := context.Background()
	alpha, beta := twoStores(t, 2)

	if err := beta.Append(ctx, "beta keeper"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	for i := range 5 {
		if err := alpha.Append(ctx, fmt.Sprintf("alpha %d", i)); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	if got := recent(t, alpha); !reflect.DeepEqual(got, []string{"alpha 3", "alpha 4"}) {
		t.Fatalf("alpha Recent() = %v, want the last two", got)
	}
	if got := recent(t, beta); !reflect.DeepEqual(got, []string{"beta keeper"}) {
		t.Fatalf("alpha's prompts evicted beta's history: Recent() = %v", got)
	}
}

// Rows written before the migration land with cwd = ” and belong to no
// directory, so a scoped store never surfaces them.
func TestLegacyRowsAreInvisibleToScopedStores(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "memory")
	legacy, err := NewStore(Config{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := legacy.Append(ctx, "an old prompt"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	scoped, err := NewStore(Config{Dir: dir, Cwd: "/work/alpha"})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = scoped.Close() })
	if got := recent(t, scoped); len(got) != 0 {
		t.Fatalf("Recent() = %v, want nothing from an unscoped legacy row", got)
	}
}
