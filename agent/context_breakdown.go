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
	// setup is what every request pays before the transcript: the prompt sections
	// and the native tool schemas. history is the transcript, the only part that
	// grows between turns.
	// appendMessageKinds appends to the list it is given, so the transcript kinds
	// start from an empty list and stay apart from the setup rows
	var setup []turn.ContextCategory
	for _, section := range sections {
		setup = addCategory(setup, section.Name, scaledTokens(int64(section.Chars), scale))
	}
	setup = addCategory(setup, "tool schemas", scaledTokens(schemaChars, scale))
	history := appendMessageKinds([]turn.ContextCategory{}, messages, scale)

	breakdown := turn.ContextBreakdown{Available: true, WindowTokens: usage.ContextWindowTokens}
	// setupEst is the fixed cost and estimated the whole legend, both before any
	// reconciliation
	var setupEst, histEst, estimated int64
	for _, category := range setup {
		setupEst += category.Tokens
	}
	for _, category := range history {
		histEst += category.Tokens
	}
	estimated = setupEst + histEst
	prompt := usage.LatestPromptTokens
	categories := append(setup, history...)
	// The estimates are calibrated, not measured, so they can overshoot the
	// provider's prompt total. The setup rows carry a per-turn estimate error
	// that a whole-legend rescale moves every turn, which reads as setup growing
	// while nothing about the request changed, so the setup rows keep their
	// estimates and only the transcript rows absorb the difference; a residual
	// the provider cannot attribute keeps its own "unattributed" row. Only when
	// the setup rows alone pass the reported total does the whole legend scale
	// down together, which is the only way to keep the bar under 100%.
	if prompt > 0 && setupEst >= prompt {
		categories = scaleCategories(categories, estimated, prompt)
	} else if prompt > 0 {
		// room is what the provider total leaves for the transcript once the
		// setup rows hold their estimates
		room := prompt-setupEst
		if histEst > room {
			history = scaleCategories(history, histEst, room)
		} else {
			history = addCategory(history, "unattributed", room-histEst)
		}
		categories = append(setup, history...)
	} else {
		breakdown.Estimated, prompt = true, estimated
	}
	breakdown.Categories = addCategory(categories, "completion", usage.LatestCompletionTokens)
	// Reasoning is completion-side: it is not carried into later prompts, so it
	// stays off the bar and out of the groups. The provider's own count wins; a
	// provider that reports none is estimated from the thinking text the session
	// already records, and the panel marks that estimate with a "~".
	// strata serves reasoning_content inside completion_tokens rather than as a
	// count of its own, so a thinking estimate sized off chars/4 alone can pass
	// that total and print a 100% share with no answer left. From a completion
	// total on, the thinking takes its share of that total by chars instead.
	// the transcript's thinking and answer char counts for the session and the
	// newest turn that thinks at all
	sessionThinking, sessionAnswer, latestThinking, latestAnswer := reasoningSizes(messages)
	breakdown.SessionReasoningTokens = cmp.Or(usage.SessionReasoningTokens,
		scaledReasoning(sessionThinking, sessionAnswer, scaledTokens(sessionThinking, scale),
			usage.SessionCompletionTokens))
	breakdown.ReasoningTokens = cmp.Or(usage.LatestReasoningTokens,
		scaledReasoning(latestThinking, latestAnswer, scaledTokens(latestThinking, scale),
			usage.LatestCompletionTokens))
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

// scaleCategories shrinks the estimated categories proportionally onto the total
// they are given, which is either the provider's prompt total or the room the
// transcript rows are left once the setup rows hold their estimates: each takes
// its exact share's floor, then the tokens the flooring left over go to the
// largest fractional remainders, so the scaled categories sum exactly. Zero-token
// rows drop out.
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

// reasoningSizes the thinking text and the answer the transcript carries on its
// assistant messages, as char counts: reasoning is completion-side, so it never
// occupies the window and stays out of the categories. The session totals and
// the newest turn that thinks at all are returned apart, and the answer counts
// size the thinking's share of the provider's completion total.
func reasoningSizes(messages []inference.Message) (sessionThinking, sessionAnswer, latestThinking, latestAnswer int64) {
	for _, message := range messages {
		if message.Reasoning == "" {
			continue
		}
		// the newest turn that thinks at all is the last one seen
		sessionThinking += int64(len(message.Reasoning))
		sessionAnswer += int64(len(message.Content))
		latestThinking, latestAnswer = int64(len(message.Reasoning)), int64(len(message.Content))
	}
	return sessionThinking, sessionAnswer, latestThinking, latestAnswer
}

// scaledReasoning sizes the thinking text against the provider's completion
// total, which already counts it: the chars/4 estimate stands on its own only
// until a completion total exists, and from then the thinking takes that total's
// share held by the thinking text's own chars, so the share stays under 100% and
// the answer keeps a line of its own. A turn that answers without thinking says
// nothing about reasoning.
func scaledReasoning(thinkingChars, answerChars, estimate, completion int64) int64 {
	if thinkingChars <= 0 || completion <= 0 {
		return estimate
	}
	share := float64(thinkingChars)/float64(thinkingChars+answerChars)
	return max(1, min(completion-1, int64(float64(completion)*share+0.5)))
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
