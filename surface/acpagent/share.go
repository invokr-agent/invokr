package acpagent

import (
	"context"
	"net"
	"sync"
	"time"

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

func (s *Sharer) serve(ctx context.Context, conn *net.UnixConn, report func(error)) {
	s.enter()
	defer s.leave()
	if err := serve(ctx, &Agent{conn: acp.NewAgentConn(conn, conn), commands: s.commands, bound: s.bound}, s.bus); err != nil {
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
