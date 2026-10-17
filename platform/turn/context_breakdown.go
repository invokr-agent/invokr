package turn

// ContextCategory is one attributed slice of the context window.
type ContextCategory struct {
	Name   string
	Tokens int64
}

// ContextBreakdown attributes context-window usage by origin for /context.
// Categories are calibrated local estimates in render order; when a provider
// prompt total exists they are reconciled against it with an "unattributed"
// residual category, otherwise Estimated is true and the numbers are raw
// estimates. Free excludes the latest completion, which is listed as its own
// category.
type ContextBreakdown struct {
	Available, Estimated     bool
	WindowTokens, FreeTokens int64
	Categories               []ContextCategory
}
