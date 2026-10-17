package messaging

import (
	"context"
	"errors"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/turn"
	"reflect"
	"testing"
)

type resumedChatSession struct {
	fakeSession
	transcript []turn.TranscriptMessage
	closed     bool
	resumeErr  error
}

func (s *resumedChatSession) ResumeTranscript(messages []turn.TranscriptMessage) error {
	s.transcript = messages
	return s.resumeErr
}
func (s *resumedChatSession) Close() { s.closed = true }

func TestReplacementWorkerRestoresTranscript(t *testing.T) {
	ctx := context.Background()
	ft := newFakeTransport("signal")
	r := makeTestRouter(t, []Transport{ft}, config.MessagingConfig{}, func(id string) (turn.IdentifiedSession, *approval.Broker, error) {
		if id == "" {
			id = "persisted-chat"
		}
		return &resumedChatSession{fakeSession: fakeSession{id: id}}, approval.NewBroker(), nil
	})
	r.app.Memory.Dir = t.TempDir()
	first, err := r.newWorker(ctx, "signal:test", ft, "test")
	if err != nil {
		t.Fatal(err)
	}
	store := sessionstore.New(r.app.Memory.Dir)
	record := sessionstore.ToolRecord{Count: 1, Evidence: []turn.ExecutionEvidence{{CallID: "old-call", Tool: "read", Outcome: "completed"}}}
	for _, msg := range []struct{ role, text string }{{"user", "remember this"}, {"assistant", "saved"}} {
		if _, err := store.AppendTurn(ctx, first.session.SessionID(), msg.role, msg.text, record); err != nil {
			t.Fatal(err)
		}
	}
	first.session.(*resumedChatSession).Close()
	second, err := r.newWorker(ctx, "signal:test", ft, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer second.session.(*resumedChatSession).Close()
	restored := second.session.(*resumedChatSession).transcript
	if first.session.SessionID() != second.session.SessionID() || len(restored) != 2 {
		t.Fatalf("replacement lost history: %+v", restored)
	}
	if restored[0].Text != "remember this" || restored[1].Text != "saved" || restored[1].ToolCallCount != 1 || !reflect.DeepEqual(restored[1].ExecutionEvidence, record.Evidence) {
		t.Fatalf("restored transcript: %+v", restored)
	}
}

func TestWorkerResumeFailureClosesSession(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("resume rejected")
	session := &resumedChatSession{fakeSession: fakeSession{id: "stored"}, resumeErr: failure}
	r := makeTestRouter(t, nil, config.MessagingConfig{}, func(string) (turn.IdentifiedSession, *approval.Broker, error) {
		return session, approval.NewBroker(), nil
	})
	if err := r.store.SetSessionID(ctx, "signal:test", "stored"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionstore.New(r.app.Memory.Dir).Append(ctx, "stored", "user", "prior"); err != nil {
		t.Fatal(err)
	}
	worker, err := r.newWorker(ctx, "signal:test", newFakeTransport("signal"), "test")
	if worker != nil || !errors.Is(err, failure) || !session.closed {
		t.Fatalf("worker=%v error=%v closed=%v", worker, err, session.closed)
	}
}
