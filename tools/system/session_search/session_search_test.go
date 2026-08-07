package session_search

import (
	"context"
	"encoding/json"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/tools/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seed writes two sessions ("old" then "new") and backdates "old" so List()
// returns "new" first deterministically (no reliance on sub-second mtimes).
func seed(t *testing.T) *sessionstore.Store {
	t.Helper()
	dir := t.TempDir()
	store := sessionstore.NewForWorkspace(dir, "/work/project")
	appendMsg(t, store, "old", "user", "let's talk about potatoes")
	appendMsg(t, store, "old", "assistant", "potatoes are green")
	appendMsg(t, store, "new", "user", "how do I deploy the parser?")
	appendMsg(t, store, "new", "assistant", "run make deploy")

	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "sessions", "old.jsonl"), past, past); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	return store
}

func appendMsg(t *testing.T, store *sessionstore.Store, id, role, text string) {
	t.Helper()
	if _, err := store.Append(context.Background(), id, role, text); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
}

func decode(t *testing.T, out string, err error, v any) {
	t.Helper()
	if err != nil {
		t.Fatalf("action error = %v", err)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
}

func TestRecentExcludesCurrentSession(t *testing.T) {
	store := seed(t)
	ctx := core.WithSessionID(context.Background(), "new")

	var resp recentResponse
	out, err := recentAction(ctx, store, Args{})
	decode(t, out, err, &resp)
	if len(resp.Sessions) != 1 || resp.Sessions[0].SessionID != "old" {
		t.Fatalf("sessions = %#v, want only the non-current 'old'", resp.Sessions)
	}
	if !strings.Contains(resp.Sessions[0].Preview, "potatoes") || resp.Sessions[0].Messages != 2 {
		t.Fatalf("summary = %#v", resp.Sessions[0])
	}
}

func TestRecentOrdersNewestFirst(t *testing.T) {
	store := seed(t)
	ctx := core.WithSessionID(context.Background(), "unrelated")

	var resp recentResponse
	out, err := recentAction(ctx, store, Args{})
	decode(t, out, err, &resp)
	if len(resp.Sessions) != 2 || resp.Sessions[0].SessionID != "new" || resp.Sessions[1].SessionID != "old" {
		t.Fatalf("order = %#v, want [new, old]", resp.Sessions)
	}
}

func TestLoadDefaultsToMostRecentExcludingCurrent(t *testing.T) {
	store := seed(t)
	ctx := core.WithSessionID(context.Background(), "new")

	var resp loadResponse
	out, err := loadAction(ctx, store, Args{})
	decode(t, out, err, &resp)
	if resp.SessionID != "old" {
		t.Fatalf("loaded %q, want 'old'", resp.SessionID)
	}
	if len(resp.Messages) != 2 || resp.Messages[0].Role != "user" || resp.Messages[0].Text != "let's talk about potatoes" {
		t.Fatalf("messages = %#v", resp.Messages)
	}
}

func TestLoadTruncatesLongMessages(t *testing.T) {
	store := sessionstore.New(t.TempDir())
	appendMsg(t, store, "s", "user", strings.Repeat("x", messageLimit+50))

	var resp loadResponse
	out, err := loadAction(context.Background(), store, Args{SessionID: "s"})
	decode(t, out, err, &resp)
	got := []rune(resp.Messages[0].Text)
	if len(got) != messageLimit+1 || !strings.HasSuffix(resp.Messages[0].Text, "…") {
		t.Fatalf("expected %d runes + ellipsis, got %d (%q)", messageLimit, len(got), resp.Messages[0].Text)
	}
}

func TestTruncateTranscriptPreservesNewestMessages(t *testing.T) {
	messages := make([]sessionstore.Message, transcriptBudget/messageLimit+1)
	for i := range messages {
		messages[i] = sessionstore.Message{Role: "user", Text: strings.Repeat("o", messageLimit)}
	}
	messages[0].Text = "oldest-" + messages[0].Text
	messages = append(messages, sessionstore.Message{Role: "assistant", Text: "newest"})
	got := truncateTranscript(messages)
	if len(got) == 0 || got[len(got)-1].Text != "newest" || strings.HasPrefix(got[0].Text, "oldest-") {
		t.Fatalf("messages = %#v, want newest included and oldest dropped", got)
	}
}

func TestSearchStillWorks(t *testing.T) {
	store := seed(t)

	var resp searchResponse
	out, err := searchAction(context.Background(), store, Args{Query: "potatoes"})
	decode(t, out, err, &resp)
	if len(resp.Hits) == 0 {
		t.Fatal("expected search hits for 'potatoes'")
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	store := seed(t)
	if _, err := searchAction(context.Background(), store, Args{}); err == nil {
		t.Fatal("expected error for empty search query")
	}
}
