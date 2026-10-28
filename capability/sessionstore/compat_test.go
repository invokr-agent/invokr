package sessionstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/platform/turn"
)

// legacyMessage is the pre-tool_calls on-disk shape, kept verbatim so the
// forward-compatibility check exercises a reader that has never heard of the
// new fields.
type legacyMessage struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

func storeWithFixture(t *testing.T, name string) *Store {
	t.Helper()
	store := New(t.TempDir())
	if err := os.MkdirAll(store.dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(name), data, 0600); err != nil {
		t.Fatal(err)
	}
	return store
}

// Backward compatibility: a transcript written before tool_calls existed still
// loads, with the new fields left zero.
func TestLoadOldFormatTranscript(t *testing.T) {
	store := storeWithFixture(t, "old-format")
	messages, err := store.Load(context.Background(), "old-format")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Text != "world" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
	if messages[1].ToolCalls != nil || messages[1].ToolCallCount != 0 {
		t.Fatalf("old format should carry no tool data: %+v", messages[1])
	}
}

func TestLoadNewFormatTranscript(t *testing.T) {
	store := storeWithFixture(t, "new-format")
	messages, err := store.Load(context.Background(), "new-format")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("unexpected messages: %+v", messages)
	}
	got := messages[1]
	if got.ToolCallCount != 3 || len(got.ToolCalls) != 1 || got.ToolCalls[0].Tool != "bash" || got.ToolCalls[0].Kind != "error" {
		t.Fatalf("tool ledger not loaded: %+v", got)
	}
}

// Forward compatibility: a line written by the new code parses cleanly with the
// legacy reader, which simply ignores the extra keys.
func TestNewWriterIsReadableByLegacyReader(t *testing.T) {
	store := New(t.TempDir())
	store.now = func() time.Time { return time.Date(2026, 6, 1, 1, 2, 3, 0, time.UTC) }
	record := ToolRecord{Calls: []turn.ToolOutcome{{Tool: "bash", Kind: "error", Detail: "exit 1"}}, Count: 2}
	if _, err := store.AppendTurn(context.Background(), "s1", "assistant", "world", record); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path("s1"))
	if err != nil {
		t.Fatal(err)
	}
	var legacy legacyMessage
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatalf("legacy reader rejected new line: %v", err)
	}
	if legacy.Role != "assistant" || legacy.Text != "world" || legacy.SessionID != "s1" {
		t.Fatalf("legacy fields lost: %+v", legacy)
	}
}

// Append (no ledger) must not emit the new keys at all, so files stay
// byte-identical in shape to what older builds produced.
func TestAppendWithoutToolsOmitsNewFields(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Append(context.Background(), "s1", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path("s1"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["tool_calls"]; ok {
		t.Fatalf("tool_calls should be omitted: %s", data)
	}
	if _, ok := raw["tool_call_count"]; ok {
		t.Fatalf("tool_call_count should be omitted: %s", data)
	}
}
