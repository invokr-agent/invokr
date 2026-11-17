package agentui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"

	tea "charm.land/bubbletea/v2"
)

// Surfaces only decide how this question is put, not what it says.
const budgetQuestion = "turn budget spent (2/2, last progress turn 1). Continue 2 more?"

// Stands in for a session whose every run exhausts its budget.
type continueGateSession struct {
	fakeSession
	gate    turn.ContinueGate
	asked   int
	granted bool
}

func (s *continueGateSession) SetContinueGate(gate turn.ContinueGate) { s.gate = gate }

func (s *continueGateSession) Send(ctx context.Context, _ string) (turn.AgentResponse, error) {
	if s.gate != nil {
		s.asked++
		s.granted = s.gate(ctx, budgetQuestion)
	}
	return turn.AgentResponse{Message: "wrapped up"}, nil
}

// There is no always option: each grant is one batch, so nothing may be remembered.
func TestContinuePromptRendersInThePanelAndAnswers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	session := &continueGateSession{}
	setContinueGate(session, panelGate(broker))
	m := newModel(ctx, session, nil, approval.NewApprovalQueue(ctx, broker), nil, false, nil, nil)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})

	answered := make(chan bool, 1)
	go func() { answered <- session.gate(ctx, budgetQuestion) }()
	waitForQueueLen(t, m.approvalQueue, 1)

	view := m.View().Content
	if !strings.Contains(view, "turn budget spent (2/2") || !strings.Contains(view, "Continue 2 more?") {
		t.Fatalf("panel does not render the question: %q", view)
	}
	if strings.Contains(view, "always") {
		t.Fatalf("panel offers an always option for the continue prompt: %q", view)
	}

	m.updateKey(testTextKey("y")) //nolint:errcheck
	select {
	case approved := <-answered:
		if !approved {
			t.Fatal("y did not approve the continue prompt")
		}
	case <-time.After(time.Second):
		t.Fatal("the run stayed blocked after y")
	}
}

// A denial answers as if there were no gate at all.
func TestContinuePromptDenyKeepsWrapUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	session := &continueGateSession{}
	setContinueGate(session, panelGate(broker))
	m := newModel(ctx, session, nil, approval.NewApprovalQueue(ctx, broker), nil, false, nil, nil)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 72, Height: 40})

	answered := make(chan bool, 1)
	go func() { answered <- session.gate(ctx, budgetQuestion) }()
	waitForQueueLen(t, m.approvalQueue, 1)

	m.updateKey(testTextKey("n")) //nolint:errcheck
	select {
	case approved := <-answered:
		if approved {
			t.Fatal("n approved the continue prompt")
		}
	case <-time.After(time.Second):
		t.Fatal("the run stayed blocked after esc")
	}
}

// Piped plain mode has no terminal to answer from.
func TestPlainContinuePromptReportsAndAnswersNo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session := &continueGateSession{}
	var out, errOut bytes.Buffer
	if err := runPlainWithApprovals(ctx, session, approval.NewBroker(), events.NewOrderedDispatcher(nil),
		strings.NewReader("go\nexit\n"), &out, &errOut); err != nil {
		t.Fatalf("plain run: %v", err)
	}
	if session.asked != 1 || session.granted {
		t.Fatalf("asked %d granted %v, want asked once and denied", session.asked, session.granted)
	}
	if !strings.Contains(errOut.String(), budgetQuestion) {
		t.Fatalf("stderr = %q, want the question reported", errOut.String())
	}
}

// TestContinuePromptCarriesThePinToTheQueue: the share answers only a pinned
// call's request, so the budget question raised by a contact's own turn must
// reach the queue flagged as pinned, while the host's own prompt stays at the
// panel for the human at the terminal.
func TestContinuePromptCarriesThePinToTheQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)
	m := newModel(ctx, &fakeSession{}, nil, queue, nil, false, nil, nil)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})

	goal := &continueGateSession{}
	setContinueGate(goal, panelGate(broker))
	answered := make(chan bool, 1)
	go func() { answered <- goal.gate(ctx, budgetQuestion) }()
	chatAnswered := make(chan bool, 1)
	go func() { chatAnswered <- goal.gate(approval.Pin(ctx), budgetQuestion) }()
	waitForQueueLen(t, queue, 2)

	queued := queue.Snapshot()
	if countPinned(queued, true) != 1 || countPinned(queued, false) != 1 {
		t.Fatalf("want one pinned and one unpinned request, got %d pinned of %d",
			countPinned(queued, true), len(queued))
	}
	for _, item := range queued {
		if item.Pending.Tool != "continue" || item.Pending.Summary != budgetQuestion {
			t.Fatalf("queued request = %q %q", item.Pending.Tool, item.Pending.Summary)
		}
	}

	m.updateKey(testTextKey("y")) //nolint:errcheck
	m.updateKey(testTextKey("y")) //nolint:errcheck
	if queue.Len() != 0 {
		t.Fatalf("queue still holds %d items", queue.Len())
	}
	select {
	case approved := <-answered:
		if !approved {
			t.Fatal("the host's own prompt was not answered at the panel")
		}
	case <-time.After(time.Second):
		t.Fatal("the host's own prompt stayed blocked")
	}
	select {
	case approved := <-chatAnswered:
		if !approved {
			t.Fatal("the pinned prompt was not answered")
		}
	case <-time.After(time.Second):
		t.Fatal("the pinned prompt stayed blocked")
	}
}

func countPinned(items []approval.QueueItem, want bool) int {
	found := 0
	for _, item := range items {
		if item.Pending.Pinned == want {
			found++
		}
	}
	return found
}
