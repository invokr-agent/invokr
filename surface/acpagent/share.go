// The session half of sharing a live TUI session with agent-relay: socket.go
// binds and verifies the socket, this file serves the ACP sessions the
// connections get. A chat contact is somebody else's words in somebody else's
// hands, so the sessions served here are pinned: they answer for their own
// approvals, and the TUI's mode, grants and toggles lend them nothing.
package acpagent

import (
	"context"
	"sync"

	"github.com/invokr-agent/acp"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

// Sharer is one shared session: the socket this TUI listens on and the host
// session the connections dialling it get.
type Sharer struct {
	socket    Socket
	bus       events.Bus
	bound     *chatSession
	commands  Commands
	mu        sync.Mutex
	connected int
	closed    bool
}

// OpenShare binds the share socket and returns the sharer. The session and
// broker are the host's own: a chat contact gets that session, so its turns
// land in the transcript, mode and approval queue the human is looking at. A
// socket file another session answers is refused rather than taken over: that
// session's chat contacts would start talking to this one.
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

// Watch accepts connections until the socket is closed, serving one ACP session
// per connection on its own thread: a chat contact that leaves must not stop
// another contact's session. report names what went wrong with a connection
// for whoever opened the share to show.
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
	// The bound agent pins turns this chat starts, so a call made on the
	// chat's behalf is answered by that chat and nothing else.
	if err := serve(&Agent{conn: acp.NewAgentConn(conn, conn), commands: s.commands, bound: s.bound}, s.bus, ctx); err != nil {
		report(err)
	}
}

// Close stops listening and unlinks the socket, so a later session can take
// the path. Connections already in flight end on their own.
func (s *Sharer) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.socket.Close()
}

// Status is what a share line shows: a bound socket waiting for agent-relay
// says listening, and a connection being served says shared.
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
