package events

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"go.uber.org/zap"
)

type OrderedDispatcher struct {
	logger     *zap.Logger
	mu         sync.RWMutex
	nextID     uint64
	registered []registeredHandler
}

type registeredHandler struct {
	id      uint64
	handler Handler
}

func NewOrderedDispatcher(logger *zap.Logger, handlers ...Handler) *OrderedDispatcher {
	logger = cmp.Or(logger, zap.NewNop())
	d := &OrderedDispatcher{logger: logger, registered: []registeredHandler{}}
	for _, handler := range handlers {
		d.Register(handler)
	}
	return d
}

func (d *OrderedDispatcher) Register(handler Handler) func() {
	if d == nil || handler == nil {
		return func() {}
	}
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	d.registered = append(d.registered, registeredHandler{id: id, handler: handler})
	d.mu.Unlock()

	return sync.OnceFunc(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.registered = slices.DeleteFunc(d.registered, func(r registeredHandler) bool { return r.id == id })
	})
}

func (d *OrderedDispatcher) Dispatch(ctx context.Context, event Event) {
	if d == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	d.mu.RLock()
	handlers := slices.Clone(d.registered)
	d.mu.RUnlock()

	for i, registered := range handlers {
		if ctx.Err() != nil {
			return
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					d.logger.Error("event handler panicked", zap.String("event_type", string(event.Type)), zap.Int("handler_index", i), zap.Any("panic", r))
				}
			}()
			if err := registered.handler.HandleEvent(ctx, event); err != nil {
				d.logger.Warn("event handler failed", zap.String("event_type", string(event.Type)), zap.Int("handler_index", i), zap.Error(err))
			}
		}()
	}
}
