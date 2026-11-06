package acpagent

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/invokr-agent/acp"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

// asks keys the open chat-side prompts by request id: one cancel per contact,
// all stood down when the human answers at the panel.
type Sharer struct {
	socket   *net.UnixListener
	path     string
	bus      events.Bus
	bound    *chatSession
	commands Commands
	mu       sync.Mutex
	live     []*Agent
	asks     map[string][]context.CancelFunc
}

const chatPermissionWait = 10 * time.Minute

const (
	// An accept failure that persists (descriptor limit, path gone) would
	// otherwise spin the watcher and flood whatever reads the report.
	acceptRetryWait    = 100 * time.Millisecond
	acceptRetryCeiling = 5 * time.Second
)

func OpenShare(path string, bus events.Bus, session turn.IdentifiedSession, approvals *approval.Broker, commands Commands) (*Sharer, error) {
	listener, err := Listen(path)
	if err != nil {
		return nil, err
	}
	return &Sharer{
		socket:   listener,
		path:     path,
		bus:      bus,
		bound:    &chatSession{IdentifiedSession: session, approvals: approvals},
		commands: commands,
		asks:     map[string][]context.CancelFunc{},
	}, nil
}

// One goroutine per connection, so an idle contact cannot block another. Closing
// the listener ends the watcher: package net reports ErrClosed to Accept.
func (s *Sharer) Watch(ctx context.Context, report func(error)) {
	wait := acceptRetryWait
	for {
		conn, err := acceptPeer(s.socket, s.path)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			report(err)
			// Back off, but stop at once when nobody wants this watcher any more.
			select {
			case <-time.After(wait):
			case <-ctx.Done():
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
	s.mu.Lock()
	s.asks[id] = append(s.asks[id], cancel)
	s.mu.Unlock()
	go func() {
		defer s.standDown(id)
		decision, answered := agent.ask(ctx, acp.SessionID(s.bound.SessionID()), req)
		if answered {
			decide(id, decision)
		}
	}()
}

// Settled is the queue's resolved callback: the human answered at the panel, so
// the chat's prompts for that request go away rather than waiting out
// chatPermissionWait for a click that means nothing now.
func (s *Sharer) Settled(id string, _ approval.Decision) {
	s.standDown(id)
}

// standDown cancels every open ask for id; a chat answer arriving later finds
// nothing to settle.
func (s *Sharer) standDown(id string) {
	s.mu.Lock()
	cancels := s.asks[id]
	delete(s.asks, id)
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// serve takes the connection's agent out of live as soon as it stops serving,
// so a contact that hung up cannot be offered a request it cannot answer.
func (s *Sharer) serve(ctx context.Context, conn *net.UnixConn, report func(error)) {
	agent := &Agent{conn: acp.NewAgentConn(conn, conn), commands: s.commands, bound: s.bound}
	s.mu.Lock()
	s.live = append(s.live, agent)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.live = slices.DeleteFunc(s.live, func(held *Agent) bool { return held == agent })
		s.mu.Unlock()
	}()
	if err := serve(ctx, agent, s.bus); err != nil {
		report(err)
	}
}

// Package net created the socket file and unlinks it on Close, which also
// unblocks a pending Accept; a bare descriptor would leave the watcher parked
// in accept() with the fd number free for reuse.
func (s *Sharer) Close() error { return s.socket.Close() }

func (s *Sharer) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.live) > 0 {
		return "shared"
	}
	return "listening"
}
