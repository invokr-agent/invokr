package file_search_test

import (
	"context"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/tools/core"
	"path/filepath"
	"testing"
)

func TestFileSearchAuditIncludesNormalizedMode(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "needle\n")
	dispatcher := events.NewOrderedDispatcher(nil)
	var seen events.Event
	dispatcher.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventAudit {
			seen = ev
		}
		return nil
	}))
	caller := newFileSearchCaller(t, root, dispatcher)

	_ = callFileSearchWithContext(t, core.WithSessionID(context.Background(), "search-mode-session"), caller, `{"query":"needle"}`)
	if seen.Audit == nil {
		t.Fatal("expected audit event")
	}
	if got := seen.Audit.Details["mode"]; got != "auto" {
		t.Fatalf("expected normalized search mode in audit details, got %v", got)
	}
}
