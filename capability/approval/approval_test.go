package approval

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBrokerApprovesPendingRequest(t *testing.T) {
	broker := NewBroker()
	requests := broker.Requests()
	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(context.Background(), Request{Tool: "shell", Summary: "Run go test"})
		done <- err
	}()

	pending := <-requests
	if pending.Tool != "shell" || pending.ID == "" {
		t.Fatalf("unexpected pending request: %#v", pending.Request)
	}
	pending.Decide(Decision{Approved: true})

	if err := <-done; err != nil {
		t.Fatalf("approval failed: %v", err)
	}
}

func TestBrokerDeniesPendingRequest(t *testing.T) {
	broker := NewBroker()
	requests := broker.Requests()
	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(context.Background(), Request{Tool: "apply_patch"})
		done <- err
	}()

	(<-requests).Decide(Decision{Approved: false, Reason: "no"})
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "approval denied") {
		t.Fatalf("expected denial error, got %v", err)
	}
}

func TestBrokerRequestHonorsContextCancellation(t *testing.T) {
	broker := NewBroker()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	_, err := broker.RequestApproval(ctx, Request{Tool: "shell"})
	if err == nil {
		t.Fatal("expected context error")
	}
}
