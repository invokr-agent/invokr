package markdown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestDocumentConcurrentAddRetainsUniqueEntries(t *testing.T) {
	doc := newTestDocument(t, "USER.md")
	ctx := context.Background()

	const count = 100
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := doc.Add(ctx, fmt.Sprintf("entry %03d", i)); err != nil {
				t.Errorf("Add(%d): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	entries, err := doc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != count {
		t.Fatalf("entry count = %d, want %d: %#v", len(entries), count, entries)
	}

	sort.Strings(entries)
	for i, entry := range entries {
		want := fmt.Sprintf("entry %03d", i)
		if entry != want {
			t.Fatalf("entry %d = %q, want %q", i, entry, want)
		}
	}
}

func TestDocumentConcurrentDuplicateAddWritesOneEntry(t *testing.T) {
	doc := newTestDocument(t, "USER.md")
	ctx := context.Background()

	const count = 64
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := doc.Add(ctx, "same durable fact"); err != nil {
				t.Errorf("Add duplicate: %v", err)
			}
		}()
	}
	wg.Wait()

	entries, err := doc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0] != "same durable fact" {
		t.Fatalf("entries = %#v, want single duplicate-collapsed entry", entries)
	}
}

func TestDocumentAddCollapsesNearDuplicate(t *testing.T) {
	doc := newTestDocument(t, "MEMORY.md")
	ctx := context.Background()
	for _, entry := range []string{"The project uses SQLite for durable storage", "This project uses SQLite for durable storage"} {
		if err := doc.Add(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := doc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("near-duplicate entries = %#v, want one", entries)
	}
}

func TestDocumentConcurrentReplaceAndRemoveExactMatches(t *testing.T) {
	doc := newTestDocument(t, "MEMORY.md")
	ctx := context.Background()

	for _, entry := range []string{"alpha fact", "beta fact", "gamma fact"} {
		if err := doc.Add(ctx, entry); err != nil {
			t.Fatalf("seed Add(%q): %v", entry, err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := doc.ReplaceByID(ctx, EntryID("alpha fact"), "alpha updated"); err != nil {
				t.Errorf("ReplaceByID: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := doc.RemoveByID(ctx, EntryID("beta fact")); err != nil {
				t.Errorf("RemoveByID: %v", err)
			}
		}()
	}
	wg.Wait()

	entries, err := doc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(entries)

	want := []string{"alpha updated", "gamma fact"}
	if strings.Join(entries, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries = %#v, want %#v", entries, want)
	}
}

func TestDocumentAtomicWriteSerializesMarkdownWithTrailingNewline(t *testing.T) {
	doc := newTestDocument(t, "MEMORY.md")
	ctx := context.Background()

	if err := doc.Add(ctx, "first fact"); err != nil {
		t.Fatalf("Add first: %v", err)
	}
	if err := doc.Add(ctx, "second fact"); err != nil {
		t.Fatalf("Add second: %v", err)
	}

	data, err := os.ReadFile(doc.path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got, want := string(data), "- first fact\n- second fact\n"; got != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}

	info, err := os.Stat(doc.path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file perm = %v, want 0600", got)
	}
}

func newTestDocument(t *testing.T, name string) *Document {
	t.Helper()
	return newMarkdownMemory(
		MemoryKindUser,
		"TEST",
		filepath.Join(t.TempDir(), name),
		10000,
	)
}
