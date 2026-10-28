package agent

import (
	"math"
	"slices"

	"github.com/invokr-agent/invokr/config"
	turnpkg "github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools/core"
)

// turnBudget replaces the runner's static turn counter with a weighted budget.
// A turn no longer costs one unit by definition: a turn every tool call of
// which policy or the user rejected costs nothing (the model never got to act
// on anything), a turn that only read costs ReadTurnWeight, and everything
// else costs one. The point is that a run's ceiling should bound the work it
// did, not the number of times it spoke.
//
// iterations is the raw loop count and is bounded separately by hardCeiling
// (MaxTurns * HardTurnMultiplier): without it a run whose turns all cost zero
// — every call denied, every turn — would never exhaust its budget and would
// spin until the wall clock or the token cap caught it.
type turnBudget struct {
	limit, spent, readWeight float64
	hardCeiling, iterations  int
	// extension is how many turns a progressing run is granted when the budget
	// runs out. The evidence for that decision is three turn indices: the last
	// turn that progressed, the last one the repeat guard nudged, and the turn
	// the previous grant was decided at (all -1 before they happen).
	extension                          int
	lastProgress, lastNudge, decidedAt int
	caps                               map[string]core.ToolCapabilities
	// seen holds every tool-call signature this run has issued, so a turn that
	// tries something new can be told from one that re-treads.
	seen map[string]bool
}

func newTurnBudget(settings config.AgentConfig, caps map[string]core.ToolCapabilities) turnBudget {
	ceiling := int(math.Ceil(float64(settings.MaxTurns) * settings.HardTurnMultiplier))
	return turnBudget{
		limit: float64(settings.MaxTurns), readWeight: settings.ReadTurnWeight,
		hardCeiling: max(ceiling, settings.MaxTurns), extension: settings.TurnExtension,
		caps: caps, seen: map[string]bool{},
		lastProgress: -1, lastNudge: -1, decidedAt: -1,
	}
}

func (b *turnBudget) exhausted() bool {
	return b.iterations >= b.hardCeiling || b.spent >= b.limit
}

// remaining is how many full-price turns are left, for the pre-exhaustion
// warning: the model is told a turn count, so the budget is rounded up.
func (b *turnBudget) remaining() int {
	return min(int(math.Ceil(b.limit-b.spent)), b.hardCeiling-b.iterations)
}

func (b *turnBudget) limitTurns() int { return int(math.Round(b.limit)) }

// observe charges one completed turn against the budget and records what it
// contributes to the next extension decision. nudged says the repeat guard
// fired on this turn.
func (b *turnBudget) observe(result turnResult, nudged bool) {
	turn := b.iterations
	b.iterations++
	b.spent += b.cost(result)
	if b.progressed(result) {
		b.lastProgress = turn
	}
	if nudged {
		b.lastNudge = turn
	}
	for _, call := range result.calls {
		b.seen[call.Key] = true
	}
}

// extend grants another TurnExtension turns when the window — the last
// TurnExtension turns, or the turns since the previous grant if that is
// shorter — shows the run is still getting somewhere: a tool call it had not
// made before, or an edit that landed, and no repeat nudge anywhere in it. A
// run that is merely re-treading falls through to graceful exhaustion, which
// is the whole point — the extension buys time for work, not for looping.
func (b *turnBudget) extend() bool {
	if b.extension <= 0 {
		return false
	}
	start := max(b.decidedAt, b.iterations-b.extension, 0)
	if b.lastNudge >= start || b.lastProgress < start {
		return false
	}
	b.limit += float64(b.extension)
	b.decidedAt = b.iterations
	return true
}

// progressed reports whether this turn did something the run had not done
// before: a tool call with a signature never issued in this run, or a write
// that was not rejected.
func (b *turnBudget) progressed(result turnResult) bool {
	for _, call := range result.calls {
		if !b.seen[call.Key] {
			return true
		}
		if b.caps[call.Name].WritesWorkspace && !failedTool(result.outcomes, call.Name) {
			return true
		}
	}
	return false
}

func failedTool(outcomes []turnpkg.ToolOutcome, tool string) bool {
	return slices.ContainsFunc(outcomes, func(o turnpkg.ToolOutcome) bool { return o.Tool == tool })
}

// cost weighs one turn. A turn with no (parseable) tool call is full price:
// the model spoke and produced nothing to act on, which is exactly the shape
// the ceiling exists to bound.
func (b *turnBudget) cost(result turnResult) float64 {
	if len(result.calls) == 0 {
		return 1
	}
	if allDenied(result) {
		return 0
	}
	if b.allReadOnly(result.calls) {
		return b.readWeight
	}
	return 1
}

// allDenied reports whether every call this turn made was refused before it
// ran, by the approval policy or by the user. Such a turn cost the run nothing
// but a completion, and charging for it punishes the operator's own policy.
func allDenied(result turnResult) bool {
	denied := 0
	for _, outcome := range result.outcomes {
		if outcome.Kind == turnpkg.OutcomeKindDenied {
			denied++
		}
	}
	return denied >= len(result.calls)
}

// allReadOnly reports whether every call this turn made was to a tool that
// declares no write, no network, and no code execution — the same capability
// flags runtime.ReadOnlyTools keys off. An unknown tool is never read-only.
func (b *turnBudget) allReadOnly(calls []toolCallSignature) bool {
	for _, call := range calls {
		capability, known := b.caps[call.Name]
		if !known || capability.WritesWorkspace || capability.Network || capability.ExecutesCode {
			return false
		}
	}
	return true
}
