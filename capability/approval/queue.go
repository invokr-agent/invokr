package approval

import (
	"context"
	"slices"
	"sync"
)

// QueueItem is one entry in the visible approval queue.
// It holds the original Pending so Decide can call p.Decide later.
type QueueItem struct {
	Pending Pending // embedded; carries Request (with ID, Tool, Summary, etc.)
	decided bool    // true once a decision has been delivered; guarded by queue mu
}

// ApprovalQueue aggregates Pending items from a Broker into a slice.
// Zero-value is not usable; construct with NewApprovalQueue.
type ApprovalQueue struct {
	Policy *Policy
	mu     sync.Mutex
	items  []QueueItem
	closed bool // true after Close(); guarded by mu
	grants *Store
}

// NewApprovalQueue creates a queue and starts draining broker.Requests() in
// the background until ctx is cancelled or the channel is closed.
func NewApprovalQueue(ctx context.Context, broker *Broker) *ApprovalQueue {
	q := &ApprovalQueue{Policy: broker.Policy}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case p, ok := <-broker.Requests():
				if !ok {
					return
				}
				q.enqueue(p)
			}
		}
	}()
	return q
}

// enqueue appends a pending request, or denies it if the queue is already
// closed — dropping it would leave the requester blocked in RequestApproval
// forever.
func (q *ApprovalQueue) enqueue(p Pending) {
	if decision, ok := q.autoDecision(p.Request); ok {
		p.Decide(decision)
		return
	}
	q.mu.Lock()
	closed := q.closed
	if !closed {
		q.items = append(q.items, QueueItem{Pending: p})
	}
	q.mu.Unlock()
	if closed {
		p.Decide(Decision{Approved: false, Reason: "approval queue closed"})
	}
}

// autoDecision preserves legacy standing decisions. Session-policy requests
// already evaluated standing rules and must receive a user's scoped decision.
func (q *ApprovalQueue) autoDecision(req Request) (Decision, bool) {
	if req.PolicyChecked {
		return Decision{}, false
	}
	q.mu.Lock()
	closed := q.closed
	grants := q.grants
	q.mu.Unlock()
	if closed {
		return Decision{}, false
	}
	return grants.Decide(req)
}

// SetStandingGrants installs (or clears with nil) the standing rule store
// consulted for future requests. Items already in the queue still require a
// user decision.
func (q *ApprovalQueue) SetStandingGrants(store *Store) {
	q.mu.Lock()
	q.grants = store
	q.mu.Unlock()
}

// Snapshot returns a copy of the current queue contents (safe to read without
// holding the lock). The returned slice is a fresh allocation; mutation is safe.
func (q *ApprovalQueue) Snapshot() []QueueItem {
	q.mu.Lock()
	out := append([]QueueItem(nil), q.items...)
	q.mu.Unlock()
	return out
}

// Len returns the current queue length (under lock; O(1)).
func (q *ApprovalQueue) Len() int {
	q.mu.Lock()
	n := len(q.items)
	q.mu.Unlock()
	return n
}

// Decide delivers a decision for the item with the given Request.ID.
// If no item with that ID is found, Decide is a no-op (idempotent).
// After calling Decide, the item is removed from the queue.
func (q *ApprovalQueue) Decide(id string, decision Decision) {
	q.mu.Lock()
	idx := slices.IndexFunc(q.items, func(item QueueItem) bool { return item.Pending.ID == id })
	if idx < 0 || q.items[idx].decided {
		q.mu.Unlock()
		return
	}
	q.items[idx].decided = true
	item := q.items[idx]
	q.items = slices.Delete(q.items, idx, idx+1)
	q.mu.Unlock()
	item.Pending.Decide(decision)
}

// DecideAll delivers the same decision to every item currently in the queue.
// Items are removed atomically under the lock before decisions are delivered.
func (q *ApprovalQueue) DecideAll(decision Decision) { q.drain(decision, false) }

// Close unblocks all pending waiters with a denial and marks the queue closed.
// Subsequent calls are no-ops (idempotent).
func (q *ApprovalQueue) Close() {
	q.drain(Decision{Approved: false, Reason: "approval queue closed"}, true)
}

// drain empties the queue under the lock and delivers decision to every item
// that had not been decided yet. With markClosed set it also closes the queue
// and returns early when it was already closed.
func (q *ApprovalQueue) drain(decision Decision, markClosed bool) {
	q.mu.Lock()
	if markClosed {
		if q.closed {
			q.mu.Unlock()
			return
		}
		q.closed = true
	}
	removed := make([]QueueItem, 0, len(q.items))
	for i := range q.items {
		if !q.items[i].decided {
			q.items[i].decided = true
			removed = append(removed, q.items[i])
		}
	}
	q.items = q.items[:0]
	q.mu.Unlock()
	for _, item := range removed {
		item.Pending.Decide(decision)
	}
}
