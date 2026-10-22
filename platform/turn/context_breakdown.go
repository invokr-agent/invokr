package turn

// ContextCategory is one attributed slice of the context window.
type ContextCategory struct {
	Name   string
	Tokens int64
}

// ContextBreakdown attributes context-window usage by origin for /usage.
// Categories are calibrated local estimates in render order; when a provider
// prompt total exists they are reconciled against it — an overshoot is scaled
// down so the legend sums to the reported truth, a residual the estimates
// cannot name is kept as an "unattributed" row — otherwise Estimated is true
// and the numbers are raw estimates. Free excludes the latest completion,
// which is listed as its own category.
//
// The reasoning pair is completion-side: reasoning is not carried into later
// prompts, so it never occupies the window and stays out of the categories.
// ReasoningTokens is the latest turn's thinking and SessionReasoningTokens the
// session's total; both are zero when there is nothing to say, and
// ReasoningEstimated marks them as char-count estimates because the provider
// reported no reasoning count of its own.
type ContextBreakdown struct {
	Available, Estimated     bool
	WindowTokens, FreeTokens int64
	ReasoningTokens, SessionReasoningTokens int64
	ReasoningEstimated              bool
	Categories                      []ContextCategory
}
