package agent

import (
	"fmt"
	"sync/atomic"

	"github.com/invokr-ai/invokr/config"
	turnpkg "github.com/invokr-ai/invokr/platform/turn"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// summarizerMeter accumulates compaction/summarizer spend. It is shared by
// pointer so spendCap copies observe one meter, and atomic because the summary
// prefetch reports from a background goroutine.
type summarizerMeter struct {
	prompt, completion atomic.Int64
}

// spendCap enforces the optional session token and cost budgets plus the
// per-request token budget. The session caps read solely provider-reported
// usage, so a provider that reports no usage leaves them inert rather than
// guessing (see exceeded). The request cap falls back to a local estimate and
// therefore holds on every provider.
type spendCap struct {
	maxTokens  int64
	maxCost    float64
	maxRequest int64
	pricing    config.PricingConfig
	// estimated accumulates a local prompt+completion token estimate for the
	// current request; it is only consulted when the provider reports no usage.
	// Runner.Run resets it at the top of every request and is the only writer,
	// so the pointer receivers below need no lock.
	estimated int64
	// summarizer meters compaction spend (ADR 0014 deferred it; issue #295).
	summarizer *summarizerMeter
	// requestCharged accumulates this request's *new* context: each turn is
	// charged its completion plus whatever the prompt grew by, never the
	// re-sent prefix the provider serves from its own cache (#445). Written by
	// observeTurn, reset by beginRequest.
	requestCharged int64
	// prevPromptTokens is the previous turn's reported prompt size, the
	// baseline the next turn's growth is measured against.
	prevPromptTokens int64
	// lastCharge is what the previous turn cost, the projection of what the
	// next one will cost (see nearRequestLimit).
	lastCharge int64
}

// beginRequest clears the per-request accounting. Called once per Run.
func (s *spendCap) beginRequest() {
	s.estimated = 0
	s.requestCharged, s.prevPromptTokens, s.lastCharge = 0, 0, 0
}

// observeSummarizer records one summarizer completion's spend. Safe from any
// goroutine (the summary prefetch runs in the background).
func (s spendCap) observeSummarizer(prompt, completion int64) {
	if s.summarizer == nil {
		return
	}
	s.summarizer.prompt.Add(prompt)
	s.summarizer.completion.Add(completion)
}

// summarized returns the session-total summarizer spend; nil-safe for
// zero-value spendCaps in tests.
func (s spendCap) summarized() (prompt, completion int64) {
	if s.summarizer == nil {
		return 0, 0
	}
	return s.summarizer.prompt.Load(), s.summarizer.completion.Load()
}

// observeEstimate accumulates the local token estimate for one model turn. It
// is the same len/4 heuristic compaction uses and is deliberately
// uncalibrated: spendCap wants a bound, not a bill.
func (s *spendCap) observeEstimate(turn ModelTurn) {
	s.lastCharge = estimatedMessageTokens(turn.Messages) +
		estimatedMessageTokenCount(inference.AssistantMessage(turn.Completion.content))
	s.estimated += s.lastCharge
}

// observeTurn charges one model turn against the request budget: its
// completion plus the growth of the prompt since the previous turn. Charging
// the whole prompt every turn re-bills the transcript prefix that is re-sent
// (and served from the provider's own cache) each time, which made a 400k cap
// bind at turn ~20 on work that was still progressing (#445).
func (s *spendCap) observeTurn(usage turnpkg.TokenUsage) {
	if !usage.Available {
		return
	}
	s.lastCharge = max(0, usage.LatestPromptTokens-s.prevPromptTokens) + usage.LatestCompletionTokens
	s.requestCharged += s.lastCharge
	s.prevPromptTokens = usage.LatestPromptTokens
}

// requestWarningPercent is how much of the request budget must be spent before
// the model is told to wrap up.
const requestWarningPercent = 85

// nearRequestLimit reports whether the request budget is within
// requestWarningPercent of being spent, so the runner can ask for a wrap-up one
// turn before the salvage path takes over (#444). The check is on *projected*
// spend — what is charged plus another turn the size of the last one — because
// the cap is tested before the warning each turn: on the actual spend a run
// whose turns are fat relative to the remaining budget jumps from under the
// threshold straight past the cap and never gets its wrap-up turn (#496).
func (s spendCap) nearRequestLimit(usage turnpkg.TokenUsage) bool {
	if s.maxRequest <= 0 {
		return false
	}
	spent := s.requestCharged
	if !usage.Available {
		spent = s.estimated
	}
	return (spent+s.lastCharge)*100 >= s.maxRequest*requestWarningPercent
}

// requestExceeded reports why this request's token budget is spent, or "".
// It prefers provider-reported per-request totals and falls back to the local
// estimate, so unlike the session caps it holds on every provider. Summarizer
// spend is deliberately excluded: compaction is the harness working around a
// small window, not the request making progress, and charging it here ended a
// marathon run at a third of its turns and wall clock (#496). It stays in the
// session token and cost caps, which is where the bill belongs.
func (s spendCap) requestExceeded(usage turnpkg.TokenUsage) string {
	if s.maxRequest <= 0 {
		return ""
	}
	// Known approximation: on a provider that reports usage on some turns only,
	// the non-reporting turns are counted by neither branch — the provider sum
	// undercounts them and the estimate is ignored. Deliberate: the cap is a
	// bound, not a bill.
	if usage.Available {
		if s.requestCharged >= s.maxRequest {
			return fmt.Sprintf("request token budget reached (%d/%d tokens)", s.requestCharged, s.maxRequest)
		}
		return ""
	}
	if s.estimated >= s.maxRequest {
		return fmt.Sprintf("request token budget reached (~%d/%d estimated tokens; provider reports no usage)", s.estimated, s.maxRequest)
	}
	return ""
}

func newSpendCap(settings config.AgentConfig, pricing config.PricingConfig, logger *zap.Logger) spendCap {
	if settings.MaxSessionCost > 0 && !pricing.Configured() {
		logger.Warn("agent.max_session_cost is set but provider.pricing is not; the cost cap is inert")
	}
	return spendCap{maxTokens: settings.MaxSessionTokens, maxCost: settings.MaxSessionCost, maxRequest: settings.MaxRequestTokens, pricing: pricing, summarizer: &summarizerMeter{}}
}

// exceeded reports why the request or session budget is spent, or "" when
// neither is.
func (s spendCap) exceeded(usage turnpkg.TokenUsage) string {
	if reason := s.requestExceeded(usage); reason != "" {
		return reason
	}
	if !usage.Available {
		return ""
	}
	prompt, cached, completion := usage.SessionTotalsSoFar()
	sumPrompt, sumCompletion := s.summarized()
	prompt += sumPrompt
	completion += sumCompletion
	if total := prompt + completion; s.maxTokens > 0 && total >= s.maxTokens {
		return fmt.Sprintf("session token budget reached (%d/%d tokens)", total, s.maxTokens)
	}
	if s.maxCost > 0 && s.pricing.Configured() {
		if cost := s.pricing.Cost(prompt, cached, completion); cost >= s.maxCost {
			return fmt.Sprintf("session cost budget reached (%.4f/%.4f)", cost, s.maxCost)
		}
	}
	return ""
}

// priced stamps the recorded session cost onto usage; a no-op when pricing is
// unconfigured, so an absent cost line means "not priced", never "free".
func (s spendCap) priced(usage turnpkg.TokenUsage) turnpkg.TokenUsage {
	if s.pricing.Configured() {
		usage.SessionCost = s.pricing.Cost(usage.SessionPromptTokens, usage.SessionCachedPromptTokens, usage.SessionCompletionTokens)
	}
	return usage
}
