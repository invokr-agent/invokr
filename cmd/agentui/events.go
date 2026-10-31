package agentui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
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
	case events.EventNotice, events.EventSteered:
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

type steerer interface {
	Steer(text string)
	Unsteer() (string, bool)
	DropSteered()
}

func (m *model) trackSteered(ev events.Event) {
	if ev.Type != events.EventSteered || ev.Notice == nil {
		return
	}
	m.steeredTurn = append(m.steeredTurn, ev.Notice.Text)
	if i := slices.Index(m.queued, ev.Notice.Text); i >= 0 {
		m.queued = slices.Delete(m.queued, i, i+1)
	}
}

func (m *model) steer(text string) {
	if s, ok := m.session.(steerer); ok {
		s.Steer(text)
	}
}

func (m *model) dropSteered() {
	if s, ok := m.session.(steerer); ok {
		s.DropSteered()
	}
}
