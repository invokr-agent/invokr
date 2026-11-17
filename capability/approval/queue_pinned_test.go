package approval

import (
	"context"
	"testing"
	"time"
)

// A pinned turn's request is answered by the chat contact that started it, never
// by this session's queue: a standing grant would settle it before the remote
// approver ever saw the prompt.
func TestQueueGrantsDoNotAnswerPinnedRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := NewBroker()
	broker.Policy = NewPolicy(Auto, false)
	queue := NewApprovalQueue(ctx, broker)
	store := newTestStore(t)
	mustAdd(t, store, testGrant(t, "git status*"))
	queue.SetStandingGrants(store)
	go func() { _, _ = broker.RequestApproval(Pin(ctx), Request{Tool: "shell", Subject: "git status"}) }()
	deadline := time.Now().Add(2 * time.Second)
	for queue.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if queue.Len() == 0 {
		t.Fatal("a standing grant answered a pinned request; nobody could ask about it")
	}
	queue.DecideAll(Decision{Approved: true})
}
