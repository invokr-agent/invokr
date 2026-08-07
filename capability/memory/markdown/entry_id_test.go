package markdown

import (
	"context"
	"testing"
)

// Regression for issue #299: a markdown record's id must be stable and distinct
// from its content, and delete/replace by that id must resolve.
func TestEntryIDIsStableAndDistinctFromContent(t *testing.T) {
	const content = "The project uses SQLite storage"
	id := EntryID(content)
	if id == content {
		t.Fatalf("EntryID(%q) = %q, want an id distinct from the content", content, id)
	}
	if id != EntryID("  The   project uses SQLite storage  ") {
		t.Fatalf("EntryID is not stable across whitespace normalization: %q", id)
	}
	if id == EntryID("something else entirely") {
		t.Fatal("EntryID collided for different content")
	}
}

func TestRemoveByIDAndReplaceByID(t *testing.T) {
	ctx := context.Background()
	memory, err := NewMemory(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	doc := memory.Project()
	if err := doc.Add(ctx, "we use SQLite for memory"); err != nil {
		t.Fatal(err)
	}

	if replaced, err := doc.ReplaceByID(ctx, EntryID("we use SQLite for memory"), "we use Postgres for memory"); err != nil || !replaced {
		t.Fatalf("ReplaceByID() = %v, %v; want true, nil", replaced, err)
	}
	entries, err := doc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != "we use Postgres for memory" {
		t.Fatalf("entries = %#v", entries)
	}

	if removed, err := doc.RemoveByID(ctx, "md-000000000000dead"); err != nil || removed {
		t.Fatalf("RemoveByID(unknown) = %v, %v; want false, nil", removed, err)
	}
	if removed, err := doc.RemoveByID(ctx, EntryID("we use Postgres for memory")); err != nil || !removed {
		t.Fatalf("RemoveByID() = %v, %v; want true, nil", removed, err)
	}
	entries, err = doc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries after delete = %#v, want empty", entries)
	}
}

// Ids minted before EntryID existed were the record text itself; they must
// still resolve so previously saved records stay deletable.
func TestRemoveByIDAcceptsLegacyContentID(t *testing.T) {
	ctx := context.Background()
	memory, err := NewMemory(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	doc := memory.User()
	if err := doc.Add(ctx, "I prefer terse answers"); err != nil {
		t.Fatal(err)
	}
	if removed, err := doc.RemoveByID(ctx, "I prefer terse answers"); err != nil || !removed {
		t.Fatalf("RemoveByID(legacy) = %v, %v; want true, nil", removed, err)
	}
}
