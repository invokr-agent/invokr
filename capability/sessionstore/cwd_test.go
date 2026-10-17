package sessionstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendStampsCwdAndListSurfacesIt(t *testing.T) {
	dir := t.TempDir()
	store := NewForWorkspace(dir, "/work/alpha")
	if _, err := store.Append(context.Background(), "s1", "user", "hello"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	messages, err := store.Load(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(messages) != 1 || messages[0].Cwd != "/work/alpha" {
		t.Fatalf("message cwd = %#v, want /work/alpha", messages)
	}
	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].Cwd != "/work/alpha" {
		t.Fatalf("session cwd = %#v, want /work/alpha", sessions)
	}
}

func TestInCwdAndFilterByCwd(t *testing.T) {
	sessions := []SessionInfo{
		{ID: "alpha", Cwd: "/work/alpha"},
		{ID: "beta", Cwd: "/work/beta"},
		{ID: "legacy"}, // written before sessions were directory-scoped
	}
	if sessions[2].InCwd("") {
		t.Fatalf("a session without a cwd must belong to no directory, not to the empty one")
	}
	kept := FilterByCwd(sessions, "/work/alpha")
	if len(kept) != 1 || kept[0].ID != "alpha" {
		t.Fatalf("FilterByCwd() = %#v, want only alpha", kept)
	}
}

func TestLastResumableInCwdSkipsOtherDirectories(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	other := NewForWorkspace(dir, "/work/beta")
	if _, err := other.Append(ctx, "beta-newest", "user", "beta"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	mine := NewForWorkspace(dir, "/work/alpha")
	if _, err := mine.Append(ctx, "alpha-older", "user", "alpha"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	// Backdate alpha so the newest transcript on disk really is beta's.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "sessions", "alpha-older.jsonl"), past, past); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	info, err := mine.LastResumableInCwd()
	if err != nil || info.ID != "alpha-older" {
		t.Fatalf("LastResumableInCwd() = %q, %v, want alpha-older", info.ID, err)
	}
	empty := NewForWorkspace(dir, "/work/gamma")
	if _, err := empty.LastResumableInCwd(); err == nil {
		t.Fatalf("LastResumableInCwd() in an unused directory should error")
	}
}

// The killed run is the one worth continuing, even when a finished session in
// the same directory is newer — that mis-attachment is what made a resume
// answer about an unrelated, already-completed task.
func TestLastResumableInCwdPrefersInterruptedSessions(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store := NewForWorkspace(dir, "/work/alpha")
	if _, err := store.Append(ctx, "killed", "user", "do the work"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if _, err := store.AppendInterrupted(ctx, "killed", "assistant", "[interrupted]", ToolRecord{}); err != nil {
		t.Fatalf("AppendInterrupted() error = %v", err)
	}
	if _, err := store.Append(ctx, "finished", "user", "something else"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	info, err := store.LastResumableInCwd()
	if err != nil || info.ID != "killed" || !info.Interrupted {
		t.Fatalf("LastResumableInCwd() = %+v, %v, want the interrupted session", info, err)
	}
	// Continuing it appends an ordinary turn, so it stops being the preferred
	// target once the work is answered.
	if _, err := store.Append(ctx, "killed", "assistant", "done"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if info, err := store.LastResumableInCwd(); err != nil || info.Interrupted {
		t.Fatalf("LastResumableInCwd() = %+v, %v, want no interrupted session left", info, err)
	}
}

func TestSearchInCwdExcludesOtherDirectories(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := NewForWorkspace(dir, "/work/beta").Append(ctx, "beta", "user", "potatoes everywhere"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	mine := NewForWorkspace(dir, "/work/alpha")
	if _, err := mine.Append(ctx, "alpha", "user", "potatoes here too"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	all, err := mine.Search(ctx, "potatoes", 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("Search() = %d hits, %v, want 2", len(all), err)
	}
	scoped, err := mine.SearchInCwd(ctx, "potatoes", 10)
	if err != nil || len(scoped) != 1 || scoped[0].SessionID != "alpha" {
		t.Fatalf("SearchInCwd() = %#v, %v, want only alpha", scoped, err)
	}
}
