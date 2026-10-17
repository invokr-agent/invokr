package agent

import (
	"context"
	"strings"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
)

// The anchor side of the replay: for every edit_file call on a recorded
// transcript, what had become of the newest prior read_file of that same path.
// This is #553's dose-response classification recomputed on real runs, and it
// is what bounded the value of #558's compaction carry (#559) — pure analysis
// over the fixtures, independent of any mechanism that has since been removed.
//
// It tracks how often compaction drops reads an edit may be anchoring on,
// and the cost in re-reads on recorded workloads.

// readState is what became of a read result, tracked across the replay.
type readState int

const (
	readVerbatim readState = iota // still in context, full text
	readDropped                   // removed from the transcript by a compaction
)

// anchorLedger classifies every edit against its path's newest prior read.
type anchorLedger struct {
	inContext   int // (a) the read is still there verbatim
	compacted   int // (b) compaction dropped it while it was still verbatim
	unread      int // (c) no prior read of that path
	rereads     int // read_file calls on a path a compaction had dropped
	compactions int
}

// Conditional anchor-miss rates from the #553/#558 marathon measurement, per
// edit call: read in context or dropped by compaction. Observational,
// and from a larger sample than these three fixtures.
const (
	missRateInContext = 0.050
	missRateCompacted = 0.188
)

// carryCeiling is the most a mechanism that carried compaction-dropped reads
// through summarization could save on this run: every (b) case converted to an
// (a) case with no other effect. An upper bound by construction — a carried
// read can also be stale, and carrying costs compactions of its own.
func (l anchorLedger) carryCeiling() float64 {
	return float64(l.compacted) * (missRateCompacted - missRateInContext)
}

// readTracker follows every read across the replay. newest maps path → the
// call id of its newest read; state maps that id → what became of it; path
// must outlive the transcript, because a dropped read's call is gone from
// toolCallsByID while its path is still what a re-read would name; justDropped
// is the set of paths a compaction removed a verbatim read of and nothing has
// re-read since — exactly what droppedReadsNotice names.
type readTracker struct {
	newest, path map[string]string
	state        map[string]readState
	justDropped  map[string]bool
}

func newReadTracker() readTracker {
	return readTracker{newest: map[string]string{}, path: map[string]string{}, state: map[string]readState{}, justDropped: map[string]bool{}}
}

// classifyTurn scores this turn's calls before it runs: each edit against its
// path's newest prior read, and each read against the paths a compaction
// dropped.
func (r readTracker) classifyTurn(calls []inference.ToolCall, ledger *anchorLedger) {
	for _, call := range calls {
		path := callPath(call)
		if path == "" {
			continue
		}
		if call.Name == readFileToolName {
			if r.justDropped[path] {
				ledger.rereads++
				delete(r.justDropped, path)
			}
			continue
		}
		if call.Name != "edit_file" && call.Name != "apply_patch" {
			continue
		}
		id, ok := r.newest[path]
		switch {
		case !ok:
			ledger.unread++
		case r.state[id] == readVerbatim:
			ledger.inContext++
		case r.state[id] == readDropped:
			ledger.compacted++
		}
	}
}

// observe records whether each read remains in the transcript or compaction dropped it.
func (r readTracker) observe(messages []inference.Message) {
	calls, present := toolCallsByID(messages), map[string]bool{}
	for _, message := range messages {
		call, ok := calls[message.ToolCallID]
		if !ok || message.Role != inference.RoleTool || call.Name != readFileToolName {
			continue
		}
		paths := readPaths(call)
		if len(paths) == 0 {
			continue
		}
		present[call.ID] = true
		r.path[call.ID] = paths[0]
		r.newest[paths[0]] = call.ID
		r.state[call.ID] = readVerbatim
	}
	for id, was := range r.state {
		if present[id] || was == readDropped {
			continue
		}
		if was == readVerbatim {
			r.justDropped[r.path[id]] = true
		}
		r.state[id] = readDropped
	}
}

// replayAnchorLedger replays a run, classifying every edit as it goes.
func replayAnchorLedger(t *testing.T, run replayRun, window int64, scale float64) anchorLedger {
	t.Helper()
	var ledger anchorLedger
	for _, segment := range run.Segments {
		var counts replayCounts
		budget := replayBudget(&counts, window)
		messages, state := replayPrefix(), contextWindowState{}
		reads := newReadTracker()
		for _, turn := range replayMessages(segment) {
			reads.classifyTurn(turn[0].ToolCalls, &ledger)
			messages, state = budget.Prepare(context.Background(), "s", append(messages, turn...), state)
			state = replayUsage(state, messages, scale)
			reads.observe(messages)
		}
		ledger.compactions += counts.complete
	}
	return ledger
}

// TestMarathonReplayAnchorLedger reports, over three real runs, how many edits
// anchor on a read still in view, one compaction dropped, or a file never
// read — plus the re-reads a dropped read provokes.
// Reporting only: every number is an observation about the fixtures, and a
// change that moves them is a change in compaction behaviour, which
// TestMarathonReplayCompactionRate already guards.
func TestMarathonReplayAnchorLedger(t *testing.T) {
	var totals anchorLedger
	for _, fixture := range replayFixtures {
		l := replayAnchorLedger(t, loadReplayRun(t, fixture), replayWindow, replayScale)
		totals.inContext += l.inContext
		totals.compacted += l.compacted
		totals.unread += l.unread
		totals.rereads += l.rereads
		totals.compactions += l.compactions
		logAnchorLedger(t, strings.TrimSuffix(fixture, ".json"), l)
	}
	logAnchorLedger(t, "TOTAL", totals)
	runs := float64(len(replayFixtures))
	t.Logf("per run: carry ceiling %.2f misses, %.2f re-reads after a drop",
		totals.carryCeiling()/runs, float64(totals.rereads)/runs)
}

func logAnchorLedger(t *testing.T, label string, l anchorLedger) {
	t.Helper()
	edits := l.inContext + l.compacted + l.unread
	t.Logf("%-10s %d compactions, %d edits: (a) in context %d, (b) compacted %d, (c) unread %d",
		label, l.compactions, edits, l.inContext, l.compacted, l.unread)
	t.Logf("%-10s ceiling: carrying (b) saves at most %.2f misses; %d re-reads after a drop",
		"", l.carryCeiling(), l.rereads)
}
