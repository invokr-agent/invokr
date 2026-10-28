package approval_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
)

func TestApprovalQueue_AccumulatesMultipleRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		}(i)
	}

	// Wait for all 3 to appear in the queue.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if q.Len() != 3 {
		t.Fatalf("expected 3 items in queue, got %d", q.Len())
	}
	q.DecideAll(approval.Decision{Approved: true})
	wg.Wait()
}

func TestApprovalQueue_DecideUnblocksToolGoroutine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	items := q.Snapshot()
	if len(items) == 0 {
		t.Fatal("expected 1 item in queue")
	}
	q.Decide(items[0].Pending.ID, approval.Decision{Approved: true})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("goroutine did not unblock")
	}
}

func TestApprovalQueue_DecideDeniedReturns_DeniedError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	items := q.Snapshot()
	if len(items) == 0 {
		t.Fatal("expected 1 item in queue")
	}
	q.Decide(items[0].Pending.ID, approval.Decision{Approved: false, Reason: "denied by user"})

	select {
	case err := <-done:
		var denied *approval.DeniedError
		if !errors.As(err, &denied) {
			t.Fatalf("expected DeniedError, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("goroutine did not unblock")
	}
}

func TestApprovalQueue_DecideAll_ResolvesAllItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
			errs <- err
		}()
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if q.Len() != 3 {
		t.Fatalf("expected 3 items, got %d", q.Len())
	}
	q.DecideAll(approval.Decision{Approved: true})

	for i := 0; i < 3; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("goroutine did not unblock")
		}
	}
}

func TestApprovalQueue_DecideAll_Deny_AllReturnDeniedError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
			errs <- err
		}()
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	q.DecideAll(approval.Decision{Approved: false, Reason: "denied by user"})

	for i := 0; i < 3; i++ {
		select {
		case err := <-errs:
			var denied *approval.DeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected DeniedError, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("goroutine did not unblock")
		}
	}
}

func TestApprovalQueue_DecideIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	items := q.Snapshot()
	id := items[0].Pending.ID

	q.Decide(id, approval.Decision{Approved: true})
	// Second call is a no-op — must not panic.
	q.Decide(id, approval.Decision{Approved: true})

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("goroutine did not unblock")
	}
}

func TestApprovalQueue_CtxCancelUnblocksGoroutine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	broker := approval.NewBroker()
	_ = approval.NewApprovalQueue(ctx, broker)

	cancel() // cancel immediately; drain goroutine exits

	// The queue is no longer draining. A RequestApproval with its own
	// cancelled ctx should return ctx.Err().
	toolCtx, toolCancel := context.WithCancel(context.Background())
	toolCancel()
	_, err := broker.RequestApproval(toolCtx, approval.Request{Tool: "shell"})
	if err == nil {
		t.Fatal("expected error from cancelled ctx")
	}
}

func TestApprovalQueue_CtxCancelOnToolGoroutine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	toolCtx, toolCancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(toolCtx, approval.Request{Tool: "shell"})
		done <- err
	}()

	// Wait for item to land in queue.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	toolCancel() // cancel the tool goroutine's ctx

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from cancelled tool ctx")
		}
	case <-time.After(time.Second):
		t.Fatal("goroutine did not unblock after ctx cancel")
	}

	// Item may still be in queue; subsequent Decide is a no-op (no crash).
	items := q.Snapshot()
	if len(items) > 0 {
		q.Decide(items[0].Pending.ID, approval.Decision{Approved: true})
	}
}

func TestApprovalQueue_Snapshot_ReturnsCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	snap := q.Snapshot()
	// Mutate the copy.
	if len(snap) > 0 {
		snap[0] = approval.QueueItem{}
	}
	// Original queue length should be unchanged.
	if q.Len() != 1 {
		t.Fatalf("expected queue len=1 after snapshot mutation, got %d", q.Len())
	}
	q.DecideAll(approval.Decision{Approved: true})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestApprovalQueue_NewArrivalAfterDecideAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	done1 := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done1 <- err
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	q.DecideAll(approval.Decision{Approved: true})
	if err := <-done1; err != nil {
		t.Fatal(err)
	}

	// New arrival after DecideAll.
	done2 := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "apply_patch"})
		done2 <- err
	}()

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if q.Len() != 1 {
		t.Fatalf("expected 1 new item after DecideAll, got %d", q.Len())
	}
	q.DecideAll(approval.Decision{Approved: true})
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
}

func TestApprovalQueue_Close_UnblocksPendingGoroutines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
			errs <- err
		}()
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if q.Len() != 3 {
		t.Fatalf("expected 3 items, got %d", q.Len())
	}

	q.Close()
	// Second Close is a no-op.
	q.Close()

	for i := 0; i < 3; i++ {
		select {
		case err := <-errs:
			var denied *approval.DeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected DeniedError, got %v", err)
			}
			if denied.Reason != "approval queue closed" {
				t.Fatalf("expected reason 'approval queue closed', got %q", denied.Reason)
			}
		case <-time.After(time.Second):
			t.Fatal("goroutine did not unblock after Close")
		}
	}
}

func TestApprovalQueue_DecideAfterClose_IsNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	items := q.Snapshot()
	id := items[0].Pending.ID

	q.Close() // resolves the item
	if err := <-done; err == nil {
		t.Fatal("expected denial after Close")
	}

	// Subsequent Decide is a no-op — must not panic.
	q.Decide(id, approval.Decision{Approved: true})
}

func TestApprovalQueue_DecidePreservesOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	q := approval.NewApprovalQueue(ctx, broker)

	// Send 3 requests sequentially so IDs are stable before we read them.
	ids := make([]string, 3)
	tools := []string{"shell", "read_file", "apply_patch"}
	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			_, err := broker.RequestApproval(ctx, approval.Request{Tool: tools[i]})
			done <- err
		}(i)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if q.Len() != 3 {
		t.Fatalf("expected 3 items, got %d", q.Len())
	}

	snap := q.Snapshot()
	ids[0] = snap[0].Pending.ID
	ids[1] = snap[1].Pending.ID
	ids[2] = snap[2].Pending.ID

	// Decide the first item; remaining two must stay in original order.
	q.Decide(ids[0], approval.Decision{Approved: true})

	snap = q.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 items after Decide, got %d", len(snap))
	}
	if snap[0].Pending.ID != ids[1] {
		t.Fatalf("expected item[0] id=%s, got %s", ids[1], snap[0].Pending.ID)
	}
	if snap[1].Pending.ID != ids[2] {
		t.Fatalf("expected item[1] id=%s, got %s", ids[2], snap[1].Pending.ID)
	}

	// Clean up.
	q.DecideAll(approval.Decision{Approved: true})
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestApprovalQueue_ModeChangePreservesPendingDecision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	q := approval.NewApprovalQueue(ctx, broker)
	toolCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	req := approval.Request{Tool: "shell", Confined: true, Writes: true}
	toolCtx, release := broker.Policy.Begin(toolCtx, req)
	defer release()
	done := make(chan error, 1)
	go func() { _, err := approval.Authorize(toolCtx, broker, req, true); done <- err }()
	for q.Len() == 0 {
		time.Sleep(time.Millisecond)
	}
	if err := broker.Policy.SetMode(approval.Auto); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 1 {
		t.Fatalf("existing unsafe request changed: queue=%d", q.Len())
	}
	q.DecideAll(approval.Decision{Approved: true, Reason: "test"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
