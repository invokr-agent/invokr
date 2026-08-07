package sessionstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreAppendAndLoad(t *testing.T) {
	store := New(t.TempDir())
	store.now = func() time.Time { return time.Date(2026, 6, 1, 1, 2, 3, 0, time.UTC) }
	if _, err := store.Append(context.Background(), "s1", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), "s1", "assistant", "world"); err != nil {
		t.Fatal(err)
	}
	messages, err := store.Load(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Text != "world" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
	if filepath.Base(store.path("../s1")) != "s1.jsonl" {
		t.Fatalf("session path not sanitized: %s", store.path("../s1"))
	}
}

func TestStoreListCountsMessages(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Append(context.Background(), "s1", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), "s1", "assistant", "world"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), "s2", "user", "hi"); err != nil {
		t.Fatal(err)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %+v", len(sessions), sessions)
	}

	counts := map[string]int{}
	for _, s := range sessions {
		counts[s.ID] = s.Messages
	}
	if counts["s1"] != 2 {
		t.Errorf("s1 message count = %d, want 2", counts["s1"])
	}
	if counts["s2"] != 1 {
		t.Errorf("s2 message count = %d, want 1", counts["s2"])
	}
}

func TestStoreLoadsLargeMessage(t *testing.T) {
	store := New(t.TempDir())
	text := strings.Repeat("x", 70<<10)
	if _, err := store.Append(context.Background(), "large", "assistant", text); err != nil {
		t.Fatal(err)
	}
	messages, err := store.Load(context.Background(), "large")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Text != text {
		t.Fatal("large transcript message did not round-trip")
	}
}

func TestStoreSearchReturnsSnippets(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Append(context.Background(), "s1", "user", "panic in approval broker"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), "s2", "user", "unrelated"); err != nil {
		t.Fatal(err)
	}
	hits, err := store.Search(context.Background(), "approval", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].SessionID != "s1" || hits[0].Snippet == "" {
		t.Fatalf("unexpected hits: %+v", hits)
	}
}
