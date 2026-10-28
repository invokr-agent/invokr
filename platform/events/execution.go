package events

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/invokr-agent/invokr/platform/turn"
)

// ExecutionLedger is request-local and observes original tool events before output gating.
type ExecutionLedger struct {
	mu         sync.Mutex
	clock      int
	records    []*turn.ExecutionEvidence
	persistent bool
}

func (l *ExecutionLedger) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clock, l.records = 0, nil
}

func (l *ExecutionLedger) Observe(kind EventType, tool ToolPayload, writes bool) {
	outcome := map[EventType]string{EventToolStarted: "running", EventToolCompleted: "completed", EventToolFailed: "failed"}[kind]
	if outcome == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clock++
	r := l.pending(tool.ID, tool.Turn)
	if kind == EventToolStarted || r == nil {
		r = &turn.ExecutionEvidence{CallID: tool.ID, Turn: tool.Turn, Tool: tool.Name, Started: l.clock, Outcome: "running", ReadOnly: !writes}
		l.records = append(l.records, r)
	}
	if kind == EventToolStarted {
		return
	}
	r.Finished, r.Outcome = l.clock, outcome
	r.Code = cmp.Or(tool.Code, r.Code)
	if kind == EventToolCompleted && r.Code != "" {
		r.Outcome = "flagged"
	}
}

type executionObserverKey struct{}

// RecordExecution carries typed facts from native owners, never tool-controlled JSON.
func RecordExecution(ctx context.Context, facts turn.ExecutionEvidence) {
	if observe, ok := ctx.Value(executionObserverKey{}).(func(turn.ExecutionEvidence)); ok {
		observe(facts)
	}
}

func (l *ExecutionLedger) Context(ctx context.Context, id string, toolTurn int) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.pending(id, toolTurn)
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, executionObserverKey{}, func(facts turn.ExecutionEvidence) {
		if facts.Persistent {
			RecordExecution(ctx, facts)
		} // Propagate child writers to the parent.
		l.mu.Lock()
		defer l.mu.Unlock()
		l.persistent = l.persistent || facts.Persistent
		r.Persistent = r.Persistent || facts.Persistent
		if !facts.Persistent {
			r.Command, r.Cwd, r.ExitCode, r.ReadOnly = facts.Command, facts.Cwd, facts.ExitCode, facts.ReadOnly
			r.Code = cmp.Or(facts.Code, r.Code)
		}
	})
}

func (l *ExecutionLedger) pending(id string, toolTurn int) *turn.ExecutionEvidence {
	for _, r := range slices.Backward(l.records) {
		if r.CallID == id && r.Turn == toolTurn && r.Finished == 0 {
			return r
		}
	}
	return nil
}

func (l *ExecutionLedger) Snapshot() []turn.ExecutionEvidence {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]turn.ExecutionEvidence, len(l.records))
	for i, record := range l.records {
		item := *record
		item.Freshness = l.freshness(record)
		if l.persistent || item.Finished == 0 {
			item.Freshness = "uncertain"
		}
		if item.ExitCode != nil {
			item.ExitCode = new(*item.ExitCode)
		}
		result[i] = item
	}
	return result
}

// Freshness is derived so native preparation can refine a conservative write declaration.
func (l *ExecutionLedger) freshness(item *turn.ExecutionEvidence) string {
	status := "current"
	for _, other := range l.records {
		if other == item || other.ReadOnly {
			continue
		}
		if item.Finished > 0 && other.Started > item.Finished {
			return "stale"
		}
		if other.Finished == 0 || other.Finished > item.Started {
			status = "uncertain"
		}
	}
	return status
}
