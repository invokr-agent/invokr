package constants

import "time"

const (
	DefaultProviderBaseURL = "http://localhost:8080"
	DefaultProviderModel   = "gpt-4o-mini"
	// DefaultAnthropicModel seeds the Anthropic profile a fresh setup writes.
	// Haiku is the cheap, fast tier, which suits a default someone lands on
	// without choosing it. It is a starting point, not a pin: "invokr config
	// profiles" shows it and the config file is the place to change it.
	DefaultAnthropicModel  = "claude-haiku-4-5-20251001"
	DefaultHTTPTimeout     = 10 * time.Minute
	DefaultProviderRetries = 2
	ContextWarningPercent  = 70
	// ContextCompactPercent is the floor for small or unknown windows and an
	// available explicit override. The default trigger reserves response room
	// from the effective window instead of fixing every run at 80%.
	ContextCompactPercent = 80
	// ContextCompactReserveTokens is the room left for one response when no
	// percent is configured; the threshold is the window less this, clamped to
	// [ContextCompactPercent, ContextCompactPercentMax] (#600).
	ContextCompactReserveTokens = 6144
	ContextCompactPercentMax    = 95
	// ContextTailPercent budgets the compaction tail: retention grows the
	// preserved recent tail until it holds this share of the context window.
	ContextTailPercent = 33
	// ToolResultMaxContextTokens is the uniform context-facing cap on a single
	// tool result at the dispatch seam. Per-tool byte caps (shell, toolbox) are
	// memory guards, not context guards; this is the one policy that bounds
	// what a result may cost the context window. Anything beyond it is dropped
	// with a marker telling the model to narrow or page.
	ToolResultMaxContextTokens = 16_000
	// ToolResultGateTokens is the default per-result token budget at the
	// dispatch gate. Results over it are reduced by the configured oversize
	// strategy (default "spill": head+tail preview + full output spilled to a
	// file the model can page with read_file). Aggressive on purpose — spilled
	// content is recoverable, unlike the hard cap above.
	ToolResultGateTokens = 4_000
	// ToolResultStrategySpill names the head+tail-preview-plus-spill-file
	// oversize strategy, the default.
	ToolResultStrategySpill = "spill"
	// ToolResultStrategyCap names the lossy hard cap (no spill file), at the
	// gate budget doubled but never above ToolResultMaxContextTokens (#536).
	// It is the default for read_file, which
	// already pages itself with offset/limit continuation markers — spilling
	// its pages to a file read back with read_file would be circular.
	ToolResultStrategyCap = "cap"
	// ToolResultKeepTurns is how many assistant turns a tool result stays
	// verbatim before the aging pass stubs it (#480). Five rather than three:
	// a read-then-plan-then-edit workflow re-reads what a 3-turn window
	// elides, and 10 of 18 edit misses in marathon M1 were on files collapsed
	// one to three turns earlier.
	ToolResultKeepTurns = 5
	// ToolResultAgeTurns is how old (in assistant turns) an eligible result must
	// get before the aging pass fires at all. Between triggers history is left
	// untouched so the provider's prompt-prefix cache survives; when it fires,
	// every result past ToolResultKeepTurns is collapsed in one batch (#480).
	ToolResultAgeTurns = 8
	// SpillDirName is the Invokr-home subdirectory holding spilled tool
	// results; it is appended to the workspace extra roots so read_file can
	// page spill files.
	SpillDirName = "spill"
	// MinimumRecommendedContextWindowTokens is the floor below which the
	// effective context window (pinned or discovered) triggers a startup
	// warning: a smaller window turns compaction into thrash (#495).
	MinimumRecommendedContextWindowTokens = 65536
)

// ContextCompactThreshold returns the default trigger for an effective window.
// An explicit agent.context_compact_percent bypasses this calculation.
func ContextCompactThreshold(window int64) int {
	if window <= 0 {
		return ContextCompactPercent
	}
	reserve := int(ContextCompactReserveTokens * 100 / window)
	return min(ContextCompactPercentMax, max(ContextCompactPercent, 100-reserve-1))
}
