package messaging

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/platform/turn"

	"go.uber.org/zap"
)

type fakeTransport struct {
	name       string
	allow      func(chatID string) bool // nil = deny everyone (fail closed)
	inbound    chan Inbound
	mu         sync.Mutex
	sent       []string
	approvals  chan approvalCall
	approveAll bool
	typing     chan bool
}

type approvalCall struct {
	chatID string
	req    ApprovalRequest
	result chan bool
}

func (f *fakeTransport) Name() string { return f.name }
func (f *fakeTransport) Allowed(chatID string) bool {
	return f.allow != nil && f.allow(chatID)
}
func (f *fakeTransport) Listen(_ context.Context) (<-chan Inbound, error) {
	return f.inbound, nil
}
func (f *fakeTransport) Send(_ context.Context, _ string, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return nil
}
func (f *fakeTransport) AskApproval(ctx context.Context, chatID string, req ApprovalRequest) (bool, error) {
	if f.approvals != nil {
		res := make(chan bool, 1)
		select {
		case f.approvals <- approvalCall{chatID, req, res}:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		select {
		case v := <-res:
			return v, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return f.approveAll, nil
}

func (f *fakeTransport) SetTyping(_ context.Context, _ string, active bool) error {
	if f.typing != nil {
		f.typing <- active
	}
	return nil
}

func (f *fakeTransport) getSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(f.sent))
	copy(cp, f.sent)
	return cp
}

func newFakeTransport(name string) *fakeTransport {
	return &fakeTransport{
		name:    name,
		allow:   func(string) bool { return true },
		inbound: make(chan Inbound, 16),
	}
}

type fakeSession struct {
	id      string
	replies []string
	mu      sync.Mutex
	idx     int
}

func (s *fakeSession) SessionID() string { return s.id }
func (s *fakeSession) Send(_ context.Context, _ string) (turn.AgentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx < len(s.replies) {
		r := s.replies[s.idx]
		s.idx++
		return turn.AgentResponse{Message: r}, nil
	}
	return turn.AgentResponse{Message: "ok"}, nil
}

func makeTestRouter(t *testing.T, transports []Transport, cfg config.MessagingConfig, sessionFunc runtime.ChatSessionFunc) *Router {
	t.Helper()
	store := newTestStore(t)
	app := config.AppConfig{Messaging: cfg, Memory: config.MemoryConfig{Dir: t.TempDir()}}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	services := runtime.Services{
		App:            app,
		Logger:         zap.NewNop(),
		Events:         bus,
		Approvals:      approval.NewBroker(),
		NewChatSession: sessionFunc,
	}
	return NewRouter(services, app, store, transports)
}

func defaultSessionFunc(reply string) runtime.ChatSessionFunc {
	return func(sessionID string) (turn.IdentifiedSession, *approval.Broker, error) {
		if sessionID == "" {
			sessionID = "sess-" + reply
		}
		return &fakeSession{id: sessionID, replies: []string{reply}}, approval.NewBroker(), nil
	}
}

// R1: first message creates session and sends reply.
func TestRouter_FirstMessageCreatesSession(t *testing.T) {
	ft := newFakeTransport("telegram")
	cfg := config.MessagingConfig{Telegram: config.TelegramConfig{AllowedChatIDs: []int64{42}}}
	r := makeTestRouter(t, []Transport{ft}, cfg, defaultSessionFunc("hello"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = r.Serve(ctx) }()
	ft.inbound <- Inbound{ChatID: "42", Text: "hi"}
	assertSent(t, ft, "hello", 2*time.Second)
}

func TestRouter_TypingCoversWholeTurn(t *testing.T) {
	ft := newFakeTransport("telegram")
	ft.typing = make(chan bool, 2)
	release := make(chan struct{})
	sessionFunc := func(string) (turn.IdentifiedSession, *approval.Broker, error) {
		return &gatedSession{id: "typing-session", release: release}, approval.NewBroker(), nil
	}
	cfg := config.MessagingConfig{Telegram: config.TelegramConfig{AllowedChatIDs: []int64{42}}}
	r := makeTestRouter(t, []Transport{ft}, cfg, sessionFunc)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = r.Serve(ctx) }()
	ft.inbound <- Inbound{ChatID: "42", Text: "hi"}

	select {
	case active := <-ft.typing:
		if !active {
			t.Fatal("typing stopped before turn began")
		}
	case <-ctx.Done():
		t.Fatal("typing did not start with turn")
	}
	close(release)
	select {
	case active := <-ft.typing:
		if active {
			t.Fatal("typing did not stop after turn")
		}
	case <-ctx.Done():
		t.Fatal("typing did not stop after turn")
	}
}

// R3: allowlist rejection — a disallowed sender gets the deny reply.
func TestRouter_AllowlistRejection(t *testing.T) {
	ft := &fakeTransport{
		name:    "telegram",
		allow:   func(chatID string) bool { return chatID == "99" },
		inbound: make(chan Inbound, 4),
	}
	r := makeTestRouter(t, []Transport{ft}, config.MessagingConfig{}, defaultSessionFunc("should-not-appear"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = r.Serve(ctx) }()
	ft.inbound <- Inbound{ChatID: "1234", Text: "hi"}
	assertSent(t, ft, "Access denied.", 2*time.Second)
}

// R3b: a transport with no allowlist opinion denies everyone (fail closed) —
// an unknown transport is never implicitly allowed.
func TestRouter_UnknownTransportDeniedByDefault(t *testing.T) {
	ft := &fakeTransport{name: "matrix", inbound: make(chan Inbound, 4)}
	r := makeTestRouter(t, []Transport{ft}, config.MessagingConfig{}, defaultSessionFunc("should-not-appear"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = r.Serve(ctx) }()
	ft.inbound <- Inbound{ChatID: "1234", Text: "hi"}
	assertSent(t, ft, "Access denied.", 2*time.Second)
}

// R3c: an allow-all transport admits chats not present on any allowlist.
func TestRouter_AllowAllAdmits(t *testing.T) {
	ft := &fakeTransport{
		name:    "telegram",
		allow:   func(string) bool { return true },
		inbound: make(chan Inbound, 4),
	}
	r := makeTestRouter(t, []Transport{ft}, config.MessagingConfig{}, defaultSessionFunc("hello"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = r.Serve(ctx) }()
	ft.inbound <- Inbound{ChatID: "1234", Text: "hi"}
	assertSent(t, ft, "hello", 2*time.Second)
}

func TestRouter_NoTransports(t *testing.T) {
	store := newTestStore(t)
	services := runtime.Services{
		Logger:         zap.NewNop(),
		Events:         events.NewOrderedDispatcher(zap.NewNop()),
		Approvals:      approval.NewBroker(),
		NewChatSession: defaultSessionFunc("x"),
	}
	r := NewRouter(services, config.AppConfig{}, store, nil)
	err := r.Serve(context.Background())
	if err == nil {
		t.Fatal("expected error for zero transports")
	}
}

// R9: inbox overflow — 9th message triggers busy reply.
func TestRouter_InboxOverflow(t *testing.T) {
	ft := newFakeTransport("signal")
	// session that blocks for a long time
	blockingFunc := func(sessionID string) (turn.IdentifiedSession, *approval.Broker, error) {
		if sessionID == "" {
			sessionID = "block-sess"
		}
		return &blockingSession{id: sessionID}, approval.NewBroker(), nil
	}
	cfg := config.MessagingConfig{Signal: config.SignalConfig{AllowedNumbers: []string{"+1test"}}}
	r := makeTestRouter(t, []Transport{ft}, cfg, blockingFunc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = r.Serve(ctx) }()

	// One message is in-flight (blocked in Send) and the inbox buffers
	// workerInboxCap more; cap+2 guarantees an overflow regardless of whether
	// the worker has dequeued the first message yet.
	for i := 0; i < workerInboxCap+2; i++ {
		ft.inbound <- Inbound{ChatID: "+1test", Text: "msg"}
	}
	// At least one "busy" should appear
	deadline := time.After(3 * time.Second)
	for {
		sent := ft.getSent()
		for _, s := range sent {
			if s == "busy — try again shortly" {
				return // success
			}
		}
		select {
		case <-deadline:
			t.Fatalf("no busy reply received; sent: %v", sent)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// blockingSession blocks in Send until context is cancelled.
type blockingSession struct{ id string }

func (s *blockingSession) SessionID() string { return s.id }
func (s *blockingSession) Send(ctx context.Context, _ string) (turn.AgentResponse, error) {
	<-ctx.Done()
	return turn.AgentResponse{}, ctx.Err()
}

type gatedSession struct {
	id      string
	release <-chan struct{}
}

func (s *gatedSession) SessionID() string { return s.id }
func (s *gatedSession) Send(context.Context, string) (turn.AgentResponse, error) {
	<-s.release
	return turn.AgentResponse{Message: "done"}, nil
}

// assertSent waits up to timeout for the transport to have sent the expected text.
func assertSent(t *testing.T, ft *fakeTransport, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		for _, s := range ft.getSent() {
			if s == want {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("expected %q in sent messages, got: %v", want, ft.getSent())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// R10: closed worker is replaced — message must not be silently lost.
func TestRoute_ClosedWorkerIsReplaced(t *testing.T) {
	ft := newFakeTransport("signal")
	cfg := config.MessagingConfig{Signal: config.SignalConfig{AllowedNumbers: []string{"+1test"}}}
	r := makeTestRouter(t, []Transport{ft}, cfg, defaultSessionFunc("pong"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = r.Serve(ctx) }()

	// First message — creates worker and gets reply.
	ft.inbound <- Inbound{ChatID: "+1test", Text: "msg1"}
	assertSent(t, ft, "pong", 2*time.Second)

	// Simulate the race: mark the worker closed before route() enqueues the next msg.
	key := "signal:+1test"
	if v, ok := r.workers.Load(key); ok {
		w := v.(*chatWorker)
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
	}

	// Second message must reach a new worker and produce a reply.
	ft.inbound <- Inbound{ChatID: "+1test", Text: "msg2"}
	deadline := time.After(2 * time.Second)
	for {
		if len(ft.getSent()) >= 2 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("second reply not received; sent: %v", ft.getSent())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Issue #327: a message already accepted into the inbox must not be dropped
// when the worker exits on idle timeout.
func TestWorker_ShutdownIdleDrainsInbox(t *testing.T) {
	ft := newFakeTransport("signal")
	cfg := config.MessagingConfig{Signal: config.SignalConfig{AllowedNumbers: []string{"+1test"}}}
	r := makeTestRouter(t, []Transport{ft}, cfg, defaultSessionFunc("pong"))
	w, err := r.newWorker(context.Background(), "signal:+1test", ft, "+1test")
	if err != nil {
		t.Fatalf("newWorker: %v", err)
	}

	// Message buffered in the inbox when the idle timer fires.
	w.inbox <- Inbound{ChatID: "+1test", Text: "buffered"}
	w.shutdownIdle(context.Background(), r)

	if got := ft.getSent(); len(got) != 1 || got[0] != "pong" {
		t.Fatalf("buffered message was dropped; sent: %v", got)
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if !closed {
		t.Fatalf("worker not marked closed after shutdownIdle")
	}
}

type evidenceSession struct{ fakeSession }

func (*evidenceSession) Send(context.Context, string) (turn.AgentResponse, error) {
	return turn.AgentResponse{Message: "Tests pass", ExecutionEvidence: []turn.ExecutionEvidence{{Tool: "shell", Outcome: "flagged", ExitCode: new(1), Freshness: "current"}}}, nil
}
func TestChatPreservesExecutionEvidence(t *testing.T) {
	ft := newFakeTransport("signal")
	r := makeTestRouter(t, []Transport{ft}, config.MessagingConfig{}, func(string) (turn.IdentifiedSession, *approval.Broker, error) {
		return &evidenceSession{}, approval.NewBroker(), nil
	})
	w, err := r.newWorker(context.Background(), "signal:test", ft, "test")
	if err != nil {
		t.Fatal(err)
	}
	w.processMessage(context.Background(), Inbound{Text: "test"}, r)
	sent := ft.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], "flagged") || !strings.Contains(sent[0], "exit=1") {
		t.Fatalf("chat dropped evidence: %v", sent)
	}
}
