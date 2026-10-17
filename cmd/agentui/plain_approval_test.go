package agentui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
)

// denyTestRequest is the approval the fake session raises.
var denyTestRequest = approval.Request{Tool: "shell", Subject: "ls -la /etc", Tier: "execute_write_command"}

// approvingSession raises an approval on every Send, like a turn that calls a
// write-tier tool.
type approvingSession struct {
	fakeSession
	broker  *approval.Broker
	lastErr error
}

func (s *approvingSession) Send(ctx context.Context, text string) (turn.AgentResponse, error) {
	_, s.lastErr = s.broker.RequestApproval(ctx, denyTestRequest)
	return turn.AgentResponse{Message: "done", ToolFailures: []turn.ToolOutcome{{Tool: "shell", Kind: turn.OutcomeKindDenied, Detail: plainDenyReason(denyTestRequest)}}}, nil
}

// Piped stdin has nobody to answer an approval: it must be auto-denied with a
// notice rather than blocking the pipeline forever.
func TestPlainModeAutoDeniesApprovals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	broker := approval.NewBroker()
	session := &approvingSession{broker: broker}
	bus := events.NewOrderedDispatcher(nil)
	var out, errOut bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runPlainWithApprovals(ctx, session, broker, bus, strings.NewReader("hi\nexit\n"), &out, &errOut)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runPlain: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("plain mode hung on an approval")
	}

	if session.lastErr == nil {
		t.Fatal("approval should have been denied")
	}
	if !strings.Contains(errOut.String(), plainDenyReason(denyTestRequest)) {
		t.Fatalf("stderr = %q, want the auto-deny notice", errOut.String())
	}
	if !strings.Contains(out.String(), plainDenyReason(denyTestRequest)) {
		t.Fatalf("stdout = %q, want the tool-outcome ledger entry", out.String())
	}
}
