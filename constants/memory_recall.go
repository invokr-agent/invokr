package constants

const (
	MemoryRecallPromptBudgetChars = 6000

	MemoryRecallTier1Backend  = "markdown"
	MemoryRecallTier2Backend  = "sqlitefts"
	MemoryRecallVectorBackend = "zerfoovector"

	MemoryRecallTier2Limit = 8

	// Recall gate (nothink probe) defaults. See docs/private/plans/nothink-recall-gate.md.
	DefaultRecallGateNothinkSuffix   = "/no_think"
	DefaultRecallGateMaxQueries      = 3
	DefaultRecallGatePerQueryLimit   = 5
	DefaultRecallGateMaxHits         = 8
	DefaultRecallGateBudgetChars     = 2000
	DefaultRecallGateMinMessageChars = 12
	DefaultRecallGateTimeoutMS       = 10000
	// RecallGateMaxTokens caps the probe completion's output tokens.
	RecallGateMaxTokens = 96
)
