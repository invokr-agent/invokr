package sqlite

import (
	"context"
	"testing"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/config"
)

func TestDeleteSessionPurgesRecordsAndEventsForThatSessionOnly(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	for _, session := range []string{"doomed", "keeper"} {
		if _, err := store.Save(ctx, core.Record{
			Scope: config.ScopeProject, Kind: core.KindFact,
			Content: "fact from " + session, SessionID: session,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendMemoryEvent(ctx, core.MemoryEventRecord{
			SessionID: session, Role: "user", Content: "event from " + session,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.DeleteSession(ctx, "doomed"); err != nil {
		t.Fatal(err)
	}

	records, err := store.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeProject}})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range records {
		if rec.SessionID == "doomed" {
			t.Fatalf("purged session still has an active record: %+v", rec)
		}
	}
	if len(records) == 0 {
		t.Fatal("purge removed the other session's records too")
	}

	doomed, err := store.CountMemoryEvents(ctx, "doomed")
	if err != nil {
		t.Fatal(err)
	}
	if doomed != 0 {
		t.Fatalf("raw events for purged session = %d, want 0", doomed)
	}
	kept, err := store.CountMemoryEvents(ctx, "keeper")
	if err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("raw events for other session = %d, want 1", kept)
	}
}

func TestDeleteSessionIgnoresBlankID(t *testing.T) {
	if err := newTestStore(t).DeleteSession(context.Background(), "  "); err != nil {
		t.Fatalf("blank session ID should be a no-op, got %v", err)
	}
}
