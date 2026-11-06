package acpagent

import (
	"context"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/invokr-agent/acp"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

type Sharer struct {
	socket   Socket
	bus      events.Bus
	bound    *chatSession
	commands Commands
	mu       sync.Mutex
	live     []*Agent
	asks     []openAsk
	closed   bool
}

// An open permission prompt on the chat side, stood down when the human answers
// at the panel so the contact cannot be left holding a settled request.
type openAsk struct {
	id     string
	cancel func()
}

const chatPermissionWait = 10 * time.Minute

const (
	// An accept failure that persists (descriptor limit, path gone) would
	// otherwise spin the watcher and flood whatever reads the report.
	acceptRetryWait    = 100 * time.Millisecond
	acceptRetryCeiling = 5 * time.Second
)

func OpenShare(path string, bus events.Bus, session turn.IdentifiedSession, approvals *approval.Broker, commands Commands) (*Sharer, error) {
	socket, err := Listen(path)
	if err != nil {
		return nil, err
	}
	return &Sharer{
		socket:   socket,
		bus:      bus,
		bound:    &chatSession{IdentifiedSession: session, approvals: approvals},
		commands: commands,
	}, nil
}

// One goroutine per connection, so an idle contact cannot block another.
func (s *Sharer) Watch(ctx context.Context, report func(error)) {
	wait := acceptRetryWait
	for {
		conn, err := s.socket.Accept()
		if err != nil {
			if s.stopped() || ctx.Err() != nil {
				return
			}
			report(err)
			if !sleepOut(ctx, wait) {
				return
			}
			wait = min(2*wait, acceptRetryCeiling)
			continue
		}
		wait = acceptRetryWait
		go s.serve(ctx, conn, report)
	}
}

// Whichever answer arrives first, the panel or the chat, settles it. Only a
// pinned call's request may travel to a chat contact: the host's own requests
// belong to the human at the terminal, whatever mode the host runs in.
func (s *Sharer) Mirror(decide func(string, approval.Decision)) func(string, approval.Request) {
	return func(id string, req approval.Request) {
		if !req.Pinned {
			return
		}
		s.mu.Lock()
		live := append([]*Agent(nil), s.live...)
		s.mu.Unlock()
		for _, agent := range live {
			s.ask(agent, id, req, decide)
		}
	}
}

func (s *Sharer) ask(agent *Agent, id string, req approval.Request, decide func(string, approval.Decision)) {
	ctx, cancel := context.WithTimeout(context.Background(), chatPermissionWait)
	s.enterAsk(id, cancel)
	go func() {
		defer s.standDown(id)
		decision, answered := agent.ask(ctx, acp.SessionID(s.bound.SessionID()), req)
		if answered {
			decide(id, decision)
		}
	}()
}

// Settled is the queue's resolved callback: the human answered at the panel, so
// the chat's prompt for that request goes away rather than waiting out
// chatPermissionWait for a click that means nothing now.
func (s *Sharer) Settled(id string, _ approval.Decision) {
	s.standDown(id)
}

func (s *Sharer) enterAsk(id string, cancel func()) {
	s.mu.Lock()
	s.asks = append(s.asks, openAsk{id: id, cancel: cancel})
	s.mu.Unlock()
}

// standDown cancels the ask for id, which ends the blocked permission request;
// a chat answer that arrives afterwards finds no ask to cancel and no request
// to settle.
func (s *Sharer) standDown(id string) {
	s.mu.Lock()
	idx := slices.IndexFunc(s.asks, func(held openAsk) bool { return held.id == id })
	var cancel func()
	if idx >= 0 {
		cancel = s.asks[idx].cancel
		s.asks = slices.Delete(s.asks, idx, idx+1)
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Sharer) pendingAsks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asks)
}

func (s *Sharer) serve(ctx context.Context, conn *net.UnixConn, report func(error)) {
	agent := &Agent{conn: acp.NewAgentConn(conn, conn), commands: s.commands, bound: s.bound}
	s.enter(agent)
	defer s.leave(agent)
	if err := serve(ctx, agent, s.bus); err != nil {
		report(err)
	}
}

func (s *Sharer) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.socket.Close()
}

func (s *Sharer) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.live) > 0 {
		return "shared"
	}
	return "listening"
}

func (s *Sharer) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Sharer) enter(agent *Agent) {
	s.mu.Lock()
	s.live = append(s.live, agent)
	s.mu.Unlock()
}

func (s *Sharer) leave(agent *Agent) {
	s.mu.Lock()
	s.live = slices.DeleteFunc(s.live, func(held *Agent) bool { return held == agent })
	s.mu.Unlock()
}

// sleepOut waits out the backoff, but ends early when ctx does, so a watcher
// nobody wants any more stops at once instead of after the ceiling.
func sleepOut(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
