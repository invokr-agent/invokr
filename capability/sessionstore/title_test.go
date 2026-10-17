package sessionstore

import (
	"context"
	"strings"
	"testing"
)

func TestTitleRoundTripAndListIgnoresSidecars(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()
	if _, err := store.Append(ctx, "sess-a", "user", "  how do I   wire the picker?  "); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "sess-a", "assistant", "like this"); err != nil {
		t.Fatal(err)
	}

	if got := store.Title("sess-a"); got != "" {
		t.Fatalf("Title before SetTitle = %q, want empty", got)
	}
	if err := store.SetTitle("sess-a", "Wiring the session picker"); err != nil {
		t.Fatal(err)
	}
	if got := store.Title("sess-a"); got != "Wiring the session picker" {
		t.Fatalf("Title = %q", got)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	// The .title sidecar must not be mistaken for a second session.
	if len(sessions) != 1 {
		t.Fatalf("List returned %d sessions, want 1: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.Title != "Wiring the session picker" {
		t.Fatalf("Title = %q", got.Title)
	}
	// Preview collapses the runs of whitespace in the stored user message.
	if got.Preview != "how do I wire the picker?" {
		t.Fatalf("Preview = %q", got.Preview)
	}
	if got.Messages != 2 {
		t.Fatalf("Messages = %d, want 2", got.Messages)
	}
	if got.Label() != got.Title {
		t.Fatalf("Label = %q, want the title", got.Label())
	}
}

func TestLabelFallsBackThroughPreviewToID(t *testing.T) {
	info := SessionInfo{ID: "abc"}
	if got := info.Label(); got != "abc" {
		t.Fatalf("Label with nothing set = %q, want the ID", got)
	}
	info.Preview = "first message"
	if got := info.Label(); got != "first message" {
		t.Fatalf("Label with preview = %q", got)
	}
	info.Title = "A Title"
	if got := info.Label(); got != "A Title" {
		t.Fatalf("Label with title = %q", got)
	}
}

// A label is cut on runes, so a multi-byte character is never split in half.
func TestTruncateLabelCutsOnRunes(t *testing.T) {
	got := truncateLabel(strings.Repeat("é", previewLimit+10))
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated label %q missing ellipsis", got)
	}
	if runes := []rune(strings.TrimSuffix(got, "…")); len(runes) != previewLimit {
		t.Fatalf("kept %d runes, want %d", len(runes), previewLimit)
	}
	if !utf8ValidString(got) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got)
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestSetTitleRejectsEmpty(t *testing.T) {
	store := New(t.TempDir())
	if err := store.SetTitle("sess-a", "   "); err == nil {
		t.Fatal("SetTitle with blank title should fail")
	}
}

func TestDeleteRemovesTranscriptAndTitle(t *testing.T) {
	store := New(t.TempDir())
	ctx := context.Background()
	for _, id := range []string{"keep", "drop"} {
		if _, err := store.Append(ctx, id, "user", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetTitle("drop", "Doomed session"); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete("drop"); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "keep" {
		t.Fatalf("after delete, sessions = %+v, want only keep", sessions)
	}
	if got := store.Title("drop"); got != "" {
		t.Fatalf("title sidecar survived deletion: %q", got)
	}
	if _, err := store.Load(ctx, "drop"); err == nil {
		t.Fatal("transcript survived deletion")
	}
}

// A session with no title sidecar must delete cleanly — most never get one.
func TestDeleteWithoutTitleSidecar(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Append(context.Background(), "s1", "user", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("s1"); err != nil {
		t.Fatalf("delete without a sidecar failed: %v", err)
	}
}

func TestDeleteUnknownSessionErrors(t *testing.T) {
	if err := New(t.TempDir()).Delete("nope"); err == nil {
		t.Fatal("deleting a missing session should error")
	}
}
