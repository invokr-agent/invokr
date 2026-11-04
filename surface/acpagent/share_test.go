// The host's own session serves a share connection, and its turns carry a pin:
// a permissive host mode must not answer for a chat contact's message.

package acpagent

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/invokr-agent/acp"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/turn"
)

// recordingSession keeps the context a turn arrived with, where the pin lives.
type recordingSession struct {
	ctx context.Context
}

func (s *recordingSession) SessionID() string { return "host-1" }

func (s *recordingSession) Send(ctx context.Context, _ string) (turn.AgentResponse, error) {
	s.ctx = ctx
	return turn.AgentResponse{Message: "done"}, nil
}

// boundAgent is the agent a shared connection gets, with a factory that fails
// the test if it is ever handed a fresh session.
func boundAgent(t *testing.T, host turn.IdentifiedSession, broker *approval.Broker) (*Agent, *chatSession) {
	chat := &chatSession{IdentifiedSession: host, approvals: broker}
	orig := latestVersion
	latestVersion = func() string { return "" }
	t.Cleanup(func() { latestVersion = orig })
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	t.Cleanup(func() {
		_ = toAgentW.Close()
		_ = toClientW.Close()
	})
	// Nothing speaks ACP back here, so drain the agent's writes to avoid blocking.
	go func() {
		into := [4096]byte{}
		for {
			read, err := toClientR.Read(into[:])
			if err != nil || read == 0 {
				return
			}
		}
	}()
	agent := &Agent{
		conn: acp.NewAgentConn(toAgentR, toClientW),
		newSession: func(string) (turn.IdentifiedSession, *approval.Broker, error) {
			t.Fatal("a shared connection made its own session")
			return nil, nil, nil
		},
		bound: chat,
	}
	return agent, chat
}

func TestSharedConnectionGetsTheHostSession(t *testing.T) {
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	agent, _ := boundAgent(t, &recordingSession{}, broker)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	resp, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: wd})
	if err != nil {
		t.Fatalf("session/new = %v", err)
	}
	if string(resp.SessionID) != "host-1" {
		t.Fatalf("session/new served session %q, want the host session host-1", resp.SessionID)
	}
}

// askingApprover records that a call reached a human rather than the host
// session's mode.
type askingApprover struct {
	asked bool
}

func (a *askingApprover) RequestApproval(context.Context, approval.Request) (approval.Decision, error) {
	a.asked = true
	return approval.Decision{Approved: false, Reason: "not now"}, nil
}

// TestChatTurnIsPinned: a pinned chat turn still asks when the host session is
// YOLO.
func TestChatTurnIsPinned(t *testing.T) {
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.YOLO, false)
	host := recordingSession{}
	agent, chat := boundAgent(t, &host, broker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if _, err := agent.send(ctx, acp.SessionID("host-1"), chat, "hello"); err != nil {
		t.Fatalf("send = %v", err)
	}
	if host.ctx == nil {
		t.Fatal("the turn never reached the host session")
	}

	asked := askingApprover{}
	policy := approval.NewPolicy(approval.YOLO, false)
	callCtx, done := policy.Begin(host.ctx, approval.Request{Tool: "shell", Subject: "git push", Writes: true, Publishes: true})
	defer done()
	if _, err := approval.Authorize(callCtx, &asked, approval.Request{Tool: "shell", Subject: "git push"}, true); err != nil && !asked.asked {
		t.Fatalf("authorize = %v", err)
	}
	if !asked.asked {
		t.Fatal("a YOLO host session answered a chat turn's call without asking")
	}
}
