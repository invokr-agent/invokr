package agentui

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/platform/turn"
)

type compactNowSession struct {
	liveCommandSession
	compactedNow int
}

func (s *compactNowSession) CompactNow(context.Context) { s.compactedNow++ }

// /compact used to only set a flag that the next model turn read, so nothing
// happened until the user sent another message.
func TestCompactRunsImmediatelyBetweenTurns(t *testing.T) {
	session := &compactNowSession{liveCommandSession: liveCommandSession{
		usage: turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 200, LatestCompletionTokens: 50},
	}}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)

	cmd, handled := m.runCommand("/compact")
	if !handled || cmd == nil {
		t.Fatalf("handled=%v cmd=%v, want a command that compacts", handled, cmd)
	}
	cmd()
	if session.compactedNow != 1 {
		t.Fatalf("CompactNow calls = %d, want 1", session.compactedNow)
	}
}

func TestCompactMidTurnStaysDeferred(t *testing.T) {
	session := &compactNowSession{liveCommandSession: liveCommandSession{
		usage: turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 200, LatestCompletionTokens: 50},
	}}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)
	m.sending = true

	if cmd, _ := m.runCommand("/compact"); cmd != nil {
		t.Fatal("mid-turn /compact must not start a compaction beside the running turn")
	}
	if session.compactedNow != 0 || session.compacts != 1 {
		t.Fatalf("CompactNow=%d requests=%d, want 0 and 1", session.compactedNow, session.compacts)
	}
}
