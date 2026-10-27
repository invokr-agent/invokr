package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
)

const (
	workerInboxCap    = 8
	workerIdleTimeout = 5 * time.Minute
	approvalTimeout   = 2 * time.Minute
	typingRefresh     = 4 * time.Second
	typingStopTimeout = 2 * time.Second
)

// Router fans in messages from all transports, routes each to a per-chat
// worker, and handles approval routing back over the originating transport.
type Router struct {
	services   runtime.Services
	app        config.AppConfig
	store      *Store
	transports []Transport
	workers    sync.Map // chatKey (string) → *chatWorker
}

type chatWorker struct {
	key, chatID string
	transport   Transport
	session     turn.IdentifiedSession
	inbox       chan Inbound
	approvals   *approval.Broker
	mu          sync.Mutex
	closed      bool
}

func NewRouter(services runtime.Services, app config.AppConfig, store *Store, transports []Transport) *Router {
	return &Router{services: services, app: app, store: store, transports: transports}
}

// Serve starts all transport Listen loops and the fan-in dispatch loop.
// Blocks until ctx is cancelled. Returns the first fatal error, or nil.
type inboundWithTransport struct {
	msg       Inbound
	transport Transport
}

func (r *Router) Serve(ctx context.Context) error {
	if len(r.transports) == 0 {
		return fmt.Errorf("messaging: no transports configured")
	}
	fanIn, err := r.startListeners(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case item := <-fanIn:
			r.route(ctx, item.transport, item.msg)
		}
	}
}

// startListeners starts every transport's listener and fans their messages into one channel.
func (r *Router) startListeners(ctx context.Context) (<-chan inboundWithTransport, error) {
	fanIn := make(chan inboundWithTransport, 64)
	for _, t := range r.transports {
		ch, err := t.Listen(ctx)
		if err != nil {
			return nil, fmt.Errorf("messaging: %s listen: %w", t.Name(), err)
		}
		go func(t Transport, ch <-chan Inbound) {
			for msg := range ch {
				select {
				case fanIn <- inboundWithTransport{msg, t}:
				case <-ctx.Done():
					return
				}
			}
		}(t, ch)
	}
	return fanIn, nil
}

func (r *Router) route(ctx context.Context, t Transport, msg Inbound) {
	if !t.Allowed(msg.ChatID) {
		_ = t.Send(ctx, msg.ChatID, "Access denied.")
		return
	}
	key := t.Name() + ":" + msg.ChatID
	for {
		w := r.getOrStartWorker(ctx, key, t, msg.ChatID)
		if w == nil {
			return
		}
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			// Evict the stale entry so getOrStartWorker can insert a replacement.
			r.workers.CompareAndDelete(key, w)
			continue
		}
		select {
		case w.inbox <- msg:
			w.mu.Unlock()
			return
		default:
		}
		w.mu.Unlock()
		r.services.Logger.Warn("chat inbox full, dropping message", zap.String("key", key))
		_ = t.Send(ctx, msg.ChatID, "busy — try again shortly")
		return
	}
}

func (r *Router) getOrStartWorker(ctx context.Context, key string, t Transport, chatID string) *chatWorker {
	if v, ok := r.workers.Load(key); ok {
		return v.(*chatWorker)
	}
	w, err := r.newWorker(ctx, key, t, chatID)
	if err != nil {
		r.services.Logger.Error("failed to create chat worker", zap.String("key", key), zap.Error(err))
		return nil
	}
	actual, loaded := r.workers.LoadOrStore(key, w)
	if loaded {
		// Lost the store race: our worker never runs, so release its session's
		// bus handlers or they leak on the shared dispatcher (#324).
		if closer, ok := w.session.(interface{ Close() }); ok {
			closer.Close()
		}
		return actual.(*chatWorker)
	}
	go w.run(ctx, r)
	return w
}

func (r *Router) newWorker(ctx context.Context, key string, t Transport, chatID string) (*chatWorker, error) {
	storedID, err := r.store.SessionID(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("store.SessionID: %w", err)
	}
	factory := r.services.NewChatSession
	sess, broker, err := factory(storedID)
	if err != nil {
		return nil, fmt.Errorf("new chat session: %w", err)
	}
	if err := r.restoreTranscript(ctx, sess, storedID); err != nil {
		if closer, ok := sess.(interface{ Close() }); ok {
			closer.Close()
		}
		return nil, fmt.Errorf("restore chat transcript: %w", err)
	}
	if storedID == "" {
		if err := r.store.SetSessionID(ctx, key, sess.SessionID()); err != nil {
			r.services.Logger.Warn("failed to persist session ID", zap.String("key", key), zap.Error(err))
		}
	}
	return &chatWorker{
		key: key, chatID: chatID, transport: t, session: sess, inbox: make(chan Inbound, workerInboxCap), approvals: broker,
	}, nil
}

// Restore before admitting messages; a stored ID can precede its first persisted turn.
func (r *Router) restoreTranscript(ctx context.Context, session turn.IdentifiedSession, id string) error {
	if id == "" {
		return nil
	}
	stored, err := sessionstore.New(r.app.Memory.Dir, r.services.Logger).Load(ctx, id)
	if errors.Is(err, sessionstore.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	resumer, ok := session.(turn.TranscriptResumer)
	if !ok {
		return fmt.Errorf("session does not support transcript resume")
	}
	return resumer.ResumeTranscript(core.MapSlice(stored, func(item sessionstore.Message) turn.TranscriptMessage {
		return turn.TranscriptMessage{Role: item.Role, Text: item.Text, ToolCalls: item.ToolCalls, ToolCallCount: item.ToolCallCount, ExecutionEvidence: item.ExecutionEvidence}
	}))
}

func (w *chatWorker) run(ctx context.Context, r *Router) {
	defer func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		r.workers.CompareAndDelete(w.key, w)
		// Release session-scoped bus handlers so idle-expired workers do not
		// accumulate toolbox refresh handlers on the shared dispatcher (#324).
		if closer, ok := w.session.(interface{ Close() }); ok {
			closer.Close()
		}
	}()
	idle := time.NewTimer(workerIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			w.shutdownIdle(ctx, r)
			return
		case msg, ok := <-w.inbox:
			if !ok {
				return
			}
			idle.Reset(workerIdleTimeout)
			w.processMessage(ctx, msg, r)
		}
	}
}

// shutdownIdle handles an idle-timeout exit. It first marks the worker closed
// under the mutex so route can no longer append to the inbox, then processes
// any messages route already accepted — otherwise a buffered message would be
// silently dropped when the select picks the idle case over a ready inbox.
func (w *chatWorker) shutdownIdle(ctx context.Context, r *Router) {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	for {
		select {
		case msg := <-w.inbox:
			w.processMessage(ctx, msg, r)
		default:
			return
		}
	}
}

func (w *chatWorker) processMessage(ctx context.Context, msg Inbound, r *Router) {
	defer func() {
		if rec := recover(); rec != nil {
			r.services.Logger.Error("chat worker panic", zap.Any("recover", rec))
			_ = w.transport.Send(ctx, w.chatID, "Internal error, please try again.")
		}
	}()

	pCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go w.drainApprovals(pCtx)
	stopTyping := w.startTyping(pCtx)
	defer stopTyping()

	resp, err := w.session.Send(pCtx, msg.Text)
	if err != nil {
		r.services.Logger.Error("session.Send error", zap.String("key", w.key), zap.Error(err))
		_ = w.transport.Send(ctx, w.chatID, "Error: "+err.Error())
		return
	}
	if sendErr := w.transport.Send(ctx, w.chatID, turn.AppendExecutionGuard(turn.AppendToolOutcomeGuard(resp.Message, resp.ToolFailures), resp.ExecutionEvidence)); sendErr != nil {
		r.services.Logger.Error("transport.Send error", zap.String("key", w.key), zap.Error(sendErr))
	}
}

type typingTransport interface {
	SetTyping(ctx context.Context, chatID string, active bool) error
}

func (w *chatWorker) startTyping(ctx context.Context) func() {
	t, ok := w.transport.(typingTransport)
	if !ok {
		return func() {}
	}
	typingCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = t.SetTyping(typingCtx, w.chatID, true)
		ticker := time.NewTicker(typingRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				_ = t.SetTyping(typingCtx, w.chatID, true)
			}
		}
	}()
	return func() {
		cancel()
		<-done
		stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), typingStopTimeout)
		defer stopCancel()
		_ = t.SetTyping(stopCtx, w.chatID, false)
	}
}

func (w *chatWorker) drainApprovals(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-w.approvals.Requests():
			if !ok {
				return
			}
			w.resolveApproval(ctx, req)
		}
	}
}

func (w *chatWorker) resolveApproval(ctx context.Context, req approval.Pending) {
	aCtx, cancel := context.WithTimeout(ctx, approvalTimeout)
	defer cancel()
	approved, err := w.transport.AskApproval(aCtx, w.chatID, ApprovalRequest{ID: req.ID, Tool: req.Tool, Summary: req.Summary, Details: req.Details})
	var reason string
	if !approved {
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
			reason = "denied by timeout"
		case err != nil:
			reason = err.Error()
		default:
			reason = "denied by user"
		}
	}
	req.Decide(approval.Decision{Approved: approved, Reason: reason})
}
