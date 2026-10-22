package agent

import (
	"cmp"
	"slices"

	"github.com/invokr-ai/invokr/capability/agentprompt"
	"github.com/invokr-ai/invokr/platform/turn"

	inference "github.com/rivt-ai/go-inference-router"
)

// promptBreakdownGateway is the optional gateway capability behind the /usage
// per-category breakdown, mirroring modelSwitcherGateway.
type promptBreakdownGateway interface {
	PromptBreakdown(sessionID string) ([]agentprompt.PromptSection, int64)
}

// ContextBreakdown attributes the context window by origin: system-prompt
// sections measured at build time, native tool schemas, and the transcript
// grouped by message kind — all sized with the same calibrated len/4 estimator
// compaction uses. When the provider has reported a prompt total, the gap
// between it and the summed estimates is surfaced as "unattributed" so the bar
// sums to the measured truth; before any report the numbers are flagged
// estimated instead.
func (s *Session) ContextBreakdown() turn.ContextBreakdown {
	gateway, ok := s.runner.modelGateway.(promptBreakdownGateway)
	if !ok {
		return turn.ContextBreakdown{}
	}
	s.runner.applyProviderContext()
	s.runner.mu.RLock()
	messages := append([]inference.Message(nil), s.runner.messages...)
	scale, usage := cmp.Or(s.runner.contextState.tokenEstimateScale, 1), s.runner.usage
	s.runner.mu.RUnlock()
	sections, schemaChars := gateway.PromptBreakdown(s.sessionID)
	return buildContextBreakdown(sections, schemaChars, messages, scale, usage)
}

// buildContextBreakdown is the pure half of ContextBreakdown, split out so the
// attribution math is testable without a full session.
func buildContextBreakdown(sections []agentprompt.PromptSection, schemaChars int64, messages []inference.Message, scale float64, usage turn.TokenUsage) turn.ContextBreakdown {
	if len(sections) == 0 && len(messages) == 0 {
		return turn.ContextBreakdown{}
	}
	var categories []turn.ContextCategory
	for _, section := range sections {
		categories = addCategory(categories, section.Name, scaledTokens(int64(section.Chars), scale))
	}
	categories = addCategory(categories, "tool schemas", scaledTokens(schemaChars, scale))
	categories = appendMessageKinds(categories, messages, scale)

	breakdown := turn.ContextBreakdown{Available: true, WindowTokens: usage.ContextWindowTokens}
	var estimated int64
	for _, category := range categories {
		estimated += category.Tokens
	}
	prompt := usage.LatestPromptTokens
	// The estimates are calibrated, not measured, so they can overshoot the
	// provider's prompt total; scale every estimated category down
	// proportionally so the legend sums to the reported truth instead of
	// going over 100%. A positive residual the provider cannot attribute keeps
	// its own "unattributed" row.
	if estimated > prompt {
		categories = scaleCategories(categories, estimated, prompt)
		estimated = prompt
	}
	if prompt > 0 {
		categories = addCategory(categories, "unattributed", prompt-estimated)
	} else {
		breakdown.Estimated, prompt = true, estimated
	}
	breakdown.Categories = addCategory(categories, "completion", usage.LatestCompletionTokens)
	// Reasoning is completion-side: it is not carried into later prompts, so it
	// stays off the bar and out of the groups. The provider's own count wins; a
	// provider that reports none is estimated from the thinking text the session
	// already records, and the panel marks that estimate with a "~".
	sessionGuess, latestGuess := reasoningSizes(messages, scale)
	breakdown.SessionReasoningTokens = cmp.Or(usage.SessionReasoningTokens, sessionGuess)
	breakdown.ReasoningTokens = cmp.Or(usage.LatestReasoningTokens, latestGuess)
	breakdown.ReasoningEstimated = usage.SessionReasoningTokens == 0 && usage.LatestReasoningTokens == 0
	breakdown.FreeTokens = max(0, usage.ContextWindowTokens-prompt-usage.LatestCompletionTokens)
	return breakdown
}

// appendMessageKinds sizes the transcript (minus the leading system message,
// attributed via prompt sections) by message kind.
func appendMessageKinds(categories []turn.ContextCategory, messages []inference.Message, scale float64) []turn.ContextCategory {
	var user, assistant, toolCalls, toolResults int64
	if len(messages) > 0 {
		messages = messages[1:]
	}
	for _, message := range messages {
		switch message.Role {
		case inference.RoleUser:
			user += int64(len(message.Content))
		case inference.RoleTool:
			toolResults += int64(len(message.Content))
		case inference.RoleAssistant:
			assistant += int64(len(message.Content))
			for _, call := range message.ToolCalls {
				toolCalls += int64(len(call.Name) + len(call.Arguments))
			}
		}
	}
	categories = addCategory(categories, "user messages", scaledTokens(user, scale))
	categories = addCategory(categories, "assistant messages", scaledTokens(assistant, scale))
	categories = addCategory(categories, "tool calls", scaledTokens(toolCalls, scale))
	return addCategory(categories, "tool results", scaledTokens(toolResults, scale))
}

// scaleCategories shrinks the estimated categories proportionally onto the
// provider's prompt total: each takes its exact share's floor, then the tokens
// the flooring left over go to the largest fractional remainders, so the scaled
// categories sum to the reported prompt exactly. Zero-token rows drop out.
func scaleCategories(categories []turn.ContextCategory, estimated, prompt int64) []turn.ContextCategory {
	if estimated <= 0 || prompt <= 0 {
		return categories
	}
	out := make([]turn.ContextCategory, len(categories))
	remainders := make([]int64, len(categories))
	order := make([]int, len(categories))
	var scaled int64
	for index, category := range categories {
		numerator := category.Tokens * prompt
		out[index] = turn.ContextCategory{Name: category.Name, Tokens: numerator / estimated}
		remainders[index] = numerator % estimated
		scaled += out[index].Tokens
		order[index] = index
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(remainders[b], remainders[a]) })
	for _, index := range order {
		if scaled >= prompt {
			break
		}
		out[index].Tokens += 1
		scaled++
	}
	return slices.DeleteFunc(out, func(category turn.ContextCategory) bool { return category.Tokens <= 0 })
}

// reasoningSizes the thinking text the transcript carries on its assistant
// messages: reasoning is completion-side, so it never occupies the window and
// stays out of the categories. The session total and the newest turn are
// returned sized with the same scale the categories use.
func reasoningSizes(messages []inference.Message, scale float64) (session, latest int64) {
	var sessionChars, latestChars int64
	for _, message := range messages {
		if message.Reasoning == "" {
			continue
		}
		sessionChars += int64(len(message.Reasoning))
		latestChars = int64(len(message.Reasoning))
	}
	return scaledTokens(sessionChars, scale), scaledTokens(latestChars, scale)
}

// addCategory appends a category, dropping empty ones so the legend stays short.
func addCategory(categories []turn.ContextCategory, name string, tokens int64) []turn.ContextCategory {
	if tokens <= 0 {
		return categories
	}
	return append(categories, turn.ContextCategory{Name: name, Tokens: tokens})
}

// scaledTokens is the calibrated len/4 estimate for a raw char count — the
// same basis as estimatedMessageTokenCount plus the EMA scale.
func scaledTokens(chars int64, scale float64) int64 {
	if chars <= 0 {
		return 0
	}
	return max(1, int64(float64((chars+3)/4)*scale))
}
