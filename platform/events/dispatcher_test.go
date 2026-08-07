package events

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

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
