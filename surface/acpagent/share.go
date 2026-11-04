package acpagent

import (
	"context"
	"sync"

	"github.com/invokr-agent/acp"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

type Sharer struct {
	socket    Socket
	bus       events.Bus
	bound     *chatSession
	commands  Commands
	mu        sync.Mutex
	connected int
	closed    bool
}

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
	for {
		conn, err := s.socket.Accept()
		if err != nil {
			if s.stopped() {
				return
			}
			report(err)
			continue
		}
		go s.serve(ctx, conn, report)
	}
}

func (s *Sharer) serve(ctx context.Context, conn *Conn, report func(error)) {
	s.enter()
	defer s.leave()
	if err := serve(&Agent{conn: acp.NewAgentConn(conn, conn), commands: s.commands, bound: s.bound}, s.bus, ctx); err != nil {
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
	if s.connected > 0 {
		return "shared"
	}
	return "listening"
}

func (s *Sharer) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Sharer) enter() {
	s.mu.Lock()
	s.connected++
	s.mu.Unlock()
}

func (s *Sharer) leave() {
	s.mu.Lock()
	s.connected--
	s.mu.Unlock()
}
