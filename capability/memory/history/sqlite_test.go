package history

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func newTestHistoryStore(t *testing.T, limit int) *Store {
	t.Helper()

	store, err := NewStore(Config{Dir: filepath.Join(t.TempDir(), "memory"), Limit: limit})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return store
}

func TestStoreAppendRecentAndDedupe(t *testing.T) {
	ctx := context.Background()
	store := newTestHistoryStore(t, 1000)

	if err := store.Append(ctx, "first"); err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	if err := store.Append(ctx, "second"); err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}
	if err := store.Append(ctx, "first"); err != nil {
		t.Fatalf("Append(duplicate) error = %v", err)
	}

	got, err := store.Recent(ctx, 1000)
	if err != nil {
		t.Fatalf("Recent() error = %v", err)
	}
	want := []string{"second", "first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Recent() = %#v, want %#v", got, want)
	}
}

func TestStorePrunesToLimit(t *testing.T) {
	ctx := context.Background()
	store := newTestHistoryStore(t, 2)

	for _, text := range []string{"one", "two", "three"} {
		if err := store.Append(ctx, text); err != nil {
			t.Fatalf("Append(%q) error = %v", text, err)
		}
	}

	got, err := store.Recent(ctx, 1000)
	if err != nil {
		t.Fatalf("Recent() error = %v", err)
	}
	want := []string{"two", "three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Recent() = %#v, want %#v", got, want)
	}
}

func TestStoreAppendIgnoresEmpty(t *testing.T) {
	ctx := context.Background()
	store := newTestHistoryStore(t, 1000)

	if err := store.Append(ctx, "  "); err != nil {
		t.Fatalf("Append(empty) error = %v", err)
	}
	got, err := store.Recent(ctx, 1000)
	if err != nil {
		t.Fatalf("Recent() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Recent() = %#v, want empty", got)
	}
}
