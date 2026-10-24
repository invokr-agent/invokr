package agentui

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
)

type toolEventQueue struct {
	mu     sync.Mutex
	events []events.Event
}

func newToolEventQueue() *toolEventQueue { return &toolEventQueue{} }

func (q *toolEventQueue) Push(ev events.Event) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.events = append(q.events, ev)
}

func (q *toolEventQueue) Drain() []events.Event {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := append([]events.Event(nil), q.events...)
	q.events = q.events[:0]
	return out
}

func registerToolEvents(eventBus events.Bus, session turn.Session, handle func(events.Event), wake func()) func() {
	if eventBus == nil || handle == nil {
		return func() {}
	}
	sessionID := ""
	if identified, ok := session.(turn.IdentifiedSession); ok {
		sessionID = identified.SessionID()
	}
	return eventBus.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if !isToolEvent(ev) {
			return nil
		}
		if sessionID != "" && ev.SessionID != sessionID {
			return nil
		}
		handle(ev)
		if wake != nil {
			wake()
		}
		return nil
	}))
}

func isToolEvent(ev events.Event) bool {
	switch ev.Type {
	case events.EventAssistantDelta:
		return ev.Delta != nil
	case events.EventNotice:
		return ev.Notice != nil
	case events.EventReasoning:
		return ev.Reasoning != nil && strings.TrimSpace(ev.Reasoning.Text) != ""
	case events.EventToolStarted, events.EventToolCompleted, events.EventToolFailed, events.EventToolProgress:
		return ev.Tool != nil
	default:
		return false
	}
}

func (m *model) trackCompaction(ev events.Event) {
	if ev.Type != events.EventNotice || ev.Notice == nil {
		return
	}
	switch {
	case ev.Notice.Text == "context compaction in progress":
		m.compactingAt = ev.Timestamp
		if m.compactingAt.IsZero() {
			m.compactingAt = time.Now()
		}
	case ev.Notice.Text == "context compaction complete", strings.HasPrefix(ev.Notice.Text, "context summarization failed;"):
		m.compactingAt = time.Time{}
	}
}
