package events

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOrderedDispatcherConcurrentRegisterUnregisterDispatch(t *testing.T) {
	var kept, transient atomic.Int64
	dispatcher := NewOrderedDispatcher(nil, HandlerFunc(func(context.Context, Event) error {
		kept.Add(1)
		return nil
	}))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			for range 100 {
				unregister := dispatcher.Register(HandlerFunc(func(context.Context, Event) error {
					transient.Add(1)
					return nil
				}))
				dispatcher.Dispatch(context.Background(), Event{Type: EventAfterResponse})
				unregister()
				unregister()
			}
		})
	}
	close(start)
	workers.Wait()
	before := transient.Load()
	dispatcher.Dispatch(context.Background(), Event{Type: EventAfterResponse})
	if got := kept.Load(); got != 801 {
		t.Fatalf("persistent handler calls = %d, want 801", got)
	}
	if before < 800 || transient.Load() != before {
		t.Fatalf("transient handler calls = %d before final dispatch, %d after", before, transient.Load())
	}
}

func TestOrderedDispatcherRunsHandlersInOrderAndContinuesAfterError(t *testing.T) {
	var got []string
	dispatcher := NewOrderedDispatcher(nil,
		HandlerFunc(func(context.Context, Event) error {
			got = append(got, "first")
			return errors.New("boom")
		}),
		HandlerFunc(func(context.Context, Event) error {
			got = append(got, "second")
			return nil
		}),
	)

	dispatcher.Dispatch(context.Background(), Event{Type: EventAfterResponse})

	if !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("handler order = %#v, want first then second", got)
	}
}

func TestOrderedDispatcherContinuesAfterPanic(t *testing.T) {
	var got []string
	dispatcher := NewOrderedDispatcher(nil,
		HandlerFunc(func(context.Context, Event) error {
			got = append(got, "first")
			panic("boom")
		}),
		HandlerFunc(func(context.Context, Event) error {
			got = append(got, "second")
			return nil
		}),
	)

	dispatcher.Dispatch(context.Background(), Event{Type: EventAfterResponse})

	if !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("handler order = %#v, want first then second", got)
	}
}

func TestOrderedDispatcherStopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var got []string
	dispatcher := NewOrderedDispatcher(nil,
		HandlerFunc(func(context.Context, Event) error {
			got = append(got, "first")
			cancel()
			return nil
		}),
		HandlerFunc(func(context.Context, Event) error {
			got = append(got, "second")
			return nil
		}),
	)

	dispatcher.Dispatch(ctx, Event{Type: EventAfterResponse})

	if !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("handler order = %#v, want only first", got)
	}
}

func TestOrderedDispatcherUnregisterRemovesHandler(t *testing.T) {
	var got []string
	dispatcher := NewOrderedDispatcher(nil)
	unregister := dispatcher.Register(HandlerFunc(func(context.Context, Event) error {
		got = append(got, "removed")
		return nil
	}))
	dispatcher.Register(HandlerFunc(func(context.Context, Event) error {
		got = append(got, "kept")
		return nil
	}))

	unregister()
	unregister()
	dispatcher.Dispatch(context.Background(), Event{Type: EventAfterResponse})

	if !reflect.DeepEqual(got, []string{"kept"}) {
		t.Fatalf("handler order = %#v, want only kept", got)
	}
}
