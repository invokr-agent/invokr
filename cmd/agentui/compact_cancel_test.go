package agentui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/invokr-agent/invokr/platform/turn"
)

type slowCompactSession struct {
	liveCommandSession
	started, cancelled         int
	entered, finished, unblock chan struct{}
}

func (s *slowCompactSession) CompactNow(ctx context.Context) {
	s.started++
	s.entered <- struct{}{}
	select {
	case <-ctx.Done():
		s.cancelled++
	case <-s.unblock:
	}
	s.finished <- struct{}{}
}

func slowSession() slowCompactSession {
	return slowCompactSession{
		liveCommandSession: liveCommandSession{
			usage: turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 200, LatestCompletionTokens: 50},
		},
		entered: make(chan struct{}), finished: make(chan struct{}), unblock: make(chan struct{}),
	}
}

func TestBackgroundCompactReportsDoneAndEscCancels(t *testing.T) {
	session := slowSession()
	m := newModel(context.Background(), &session, nil, nil, nil, false, nil, nil)
	cmd, handled := m.runCommand("/compact")
	if !handled || cmd == nil {
		t.Fatalf("handled=%v cmd=%v, want a background compaction", handled, cmd)
	}
	produced := make(chan tea.Msg)
	go func() { produced <- cmd() }()
	<-session.entered
	if !m.compacting || m.cancelSend == nil {
		t.Fatalf("compacting=%v cancellable=%v, want a cancellable compaction", m.compacting, m.cancelSend != nil)
	}
	_, _ = m.handleInterrupt()
	var ended bool
	select {
	case <-session.finished:
		ended = true
	case <-time.After(5 * time.Second):
	}
	if !ended || session.cancelled != 1 {
		t.Fatalf("ended=%v cancelled=%d, want esc to cancel the compaction", ended, session.cancelled)
	}
	msg := <-produced
	if _, ok := msg.(compactionMsg); !ok {
		t.Fatalf("command finished with %v, want a completion message", msg)
	}
	if _, next := m.updateCompaction(); next != nil {
		t.Fatalf("flush=%v, want a cancelled compaction to leave the queue alone", next != nil)
	}
	if m.compacting || !m.compactingAt.IsZero() || m.stopping {
		t.Fatalf("compacting=%v compactingAt=%v stopping=%v, want the compaction state closed", m.compacting, !m.compactingAt.IsZero(), m.stopping)
	}
}

func TestCompactionReleasesQueuedPrompt(t *testing.T) {
	session := slowSession()
	m := newModel(context.Background(), &session, nil, nil, nil, false, nil, nil)
	cmd, _ := m.runCommand("/compact")
	produced := make(chan tea.Msg)
	go func() { produced <- cmd() }()
	<-session.entered
	m.queued = []string{"queued while compacting"}
	session.unblock <- struct{}{}
	<-session.finished
	<-produced
	_, next := m.updateCompaction()
	if next == nil || len(m.queued) != 0 {
		t.Fatalf("queued=%v flushed=%v, want the queued prompt sent once compaction ends", m.queued, next != nil)
	}
}
