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

type recordingSession struct {
	ctx context.Context
}

func (s *recordingSession) SessionID() string { return "host-1" }

func (s *recordingSession) Send(ctx context.Context, _ string) (turn.AgentResponse, error) {
	s.ctx = ctx
	return turn.AgentResponse{Message: "done"}, nil
}

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

type askingApprover struct {
	asked bool
}

func (a *askingApprover) RequestApproval(context.Context, approval.Request) (approval.Decision, error) {
	a.asked = true
	return approval.Decision{Approved: false, Reason: "not now"}, nil
}

func TestSharedConnectionCannotWidenTheHostMode(t *testing.T) {
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	agent, chat := boundAgent(t, &recordingSession{}, broker)
	agent.sessions.Store(acp.SessionID("host-1"), chat)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, widen := range []string{"auto", "yolo", "manual"} {
		if _, err := agent.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionID: acp.SessionID("host-1"), ModeID: acp.SessionModeID(widen)}); err == nil {
			t.Fatalf("a shared connection set the host's mode to %s", widen)
		}
	}
	if broker.Policy.Mode() != approval.Manual {
		t.Fatalf("the host's mode became %s", broker.Policy.Mode())
	}
	if _, err := agent.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionID: acp.SessionID("host-1"), ModeID: "plan"}); err != nil {
		t.Fatalf("narrowing the host to plan = %v", err)
	}
	if broker.Policy.Mode() != approval.Plan {
		t.Fatalf("plan was refused: the host is in %s", broker.Policy.Mode())
	}
}

// A shared turn's commands must not be able to hand work to the host unpinned:
// the turn is a chat contact's, and its approvals are the human's.
func TestSharedTurnPinsWhatItsCommandsStart(t *testing.T) {
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.YOLO, false)
	agent, _ := boundAgent(t, &recordingSession{}, broker)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commands := pinProbe{}
	agent.commands = &commands
	if _, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: wd}); err != nil {
		t.Fatalf("session/new = %v", err)
	}
	if _, err := agent.Prompt(ctx, acp.PromptRequest{SessionID: acp.SessionID("host-1"), Prompt: []acp.ContentBlock{
		{Text: &acp.TextContent{Text: "/usage hi"}}}}); err != nil {
		t.Fatalf("prompt = %v", err)
	}
	if !commands.pinned {
		t.Fatal("a shared turn handed its commands an unpinned context")
	}
}

func TestSharedConnectionCannotStartScheduledJobs(t *testing.T) {
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	agent, _ := boundAgent(t, &recordingSession{}, broker)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	commands := pinProbe{}
	agent.commands = &commands
	if _, err := agent.NewSession(ctx, acp.NewSessionRequest{Cwd: wd}); err != nil {
		t.Fatalf("session/new = %v", err)
	}
	if _, err := agent.Prompt(ctx, acp.PromptRequest{SessionID: acp.SessionID("host-1"), Prompt: []acp.ContentBlock{
		{Text: &acp.TextContent{Text: "/jobs run nightly"}}}}); err != nil {
		t.Fatalf("prompt = %v", err)
	}
	if commands.called {
		t.Fatal("the shared turn reached the command backend")
	}

	// The human at the terminal still runs their own jobs.
	agent.bound = nil
	if _, err := agent.Prompt(ctx, acp.PromptRequest{SessionID: acp.SessionID("host-1"), Prompt: []acp.ContentBlock{
		{Text: &acp.TextContent{Text: "/jobs run nightly"}}}}); err != nil {
		t.Fatalf("prompt = %v", err)
	}
	if !commands.called {
		t.Fatal("a job stopped reaching the host's own command backend")
	}
}

type pinProbe struct {
	pinned bool
	called bool
}

func (pinProbe) Available() []acp.AvailableCommand { return nil }

func (p *pinProbe) Run(ctx context.Context, _ turn.Session, _ string) (string, string, bool) {
	p.pinned = approval.Pinned(ctx)
	p.called = true
	return "command ran", "", true
}

func TestWatchStopsWhenTheSharerCloses(t *testing.T) {
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	socket := socketPathIn(t)
	sharer, err := OpenShare(socket, nil, &recordingSession{}, broker, nil)
	if err != nil {
		t.Fatalf("open share = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopped := make(chan bool, 1)
	go func() {
		sharer.Watch(ctx, func(error) {})
		stopped <- true
	}()
	peer := dial(t, socket)
	deadline := time.Now().Add(5 * time.Second)
	for sharer.Status() != "shared" {
		if time.Now().After(deadline) {
			t.Fatal("the connection was never served")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = peer.Close()
	deadline = time.Now().Add(5 * time.Second)
	for sharer.Status() != "listening" {
		if time.Now().After(deadline) {
			t.Fatal("the served connection never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := sharer.Close(); err != nil {
		t.Fatalf("close = %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch is still accepting after the sharer closed")
	}
}

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

// The human answering at the panel must stand every chat prompt down: no contact
// can be left holding a request that is already settled, and its answer must
// not settle it a second time.
func TestPanelAnswerStandsDownEveryChatAsk(t *testing.T) {
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	sharer, err := OpenShare(socketPathIn(t), nil, &recordingSession{}, broker, nil)
	if err != nil {
		t.Fatalf("open share = %v", err)
	}
	defer func() { _ = sharer.Close() }()
	for range 2 {
		// Two contacts, one shared session: both are asked about the same call.
		agent, _ := boundAgent(t, &recordingSession{}, broker)
		sharer.ask(agent, "call-1", approval.Request{Tool: "shell", Subject: "git push"},
			func(_ string, _ approval.Decision) {
				t.Fatal("the chat answered a request the panel had already settled")
			})
	}
	if open := openAsks(sharer); open != 2 {
		t.Fatalf("%d chat asks open, want one per contact", open)
	}
	sharer.Settled("call-1", approval.Decision{Approved: true})
	if open := openAsks(sharer); open != 0 {
		t.Fatalf("%d chat asks still open after the panel answered, want none", open)
	}
}

// openAsks counts the chat prompts a sharer still holds: one entry per contact
// asked about a request, keyed by that request's id.
func openAsks(s *Sharer) int {
	s.mu.Lock()
	var open int
	for _, cancels := range s.asks {
		open += len(cancels)
	}
	s.mu.Unlock()
	return open
}
