package agentui

import (
	"context"
	"github.com/invokr-ai/invokr/platform/events"
	"strings"
	"testing"
	"time"
)

func TestCompactionStatusLifecycle(t *testing.T) {
	m := newModel(context.Background(), nil, newToolEventQueue(), nil, nil, false, nil, nil)
	m.sending = true
	start := time.Now().Add(-5 * time.Second)
	m.toolEvents.Push(events.Event{Type: events.EventNotice, Timestamp: start, Notice: &events.NoticePayload{Text: "context compaction in progress"}})
	m.drainToolEvents()
	if !m.compactingAt.Equal(start) {
		t.Fatal("start time lost")
	}
	if got := m.contextStatus(100); !strings.Contains(got, "compacting") || !strings.Contains(got, "5") {
		t.Fatalf("status %q", got)
	}
	if got := m.chatContent(); !strings.Contains(got, "compacting context") {
		t.Fatalf("chat %q", got)
	}
	for _, notice := range []string{"context compaction complete", "context summarization failed; elided old tool output"} {
		m.compactingAt = start
		m.toolEvents.Push(events.Event{Type: events.EventNotice, Notice: &events.NoticePayload{Text: notice}})
		m.drainToolEvents()
		if !m.compactingAt.IsZero() {
			t.Fatalf("timer retained after %s", notice)
		}
	}
	m.compactingAt = start
	m.updateResponse(responseMsg{err: context.Canceled})
	if !m.compactingAt.IsZero() {
		t.Fatal("timer retained after cancellation")
	}
}
