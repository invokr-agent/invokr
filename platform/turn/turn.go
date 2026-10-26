// Package turn holds the runtime turn/session protocol types shared between
// the agent runner, the TUI, and other callers. These are not configuration —
// they live here rather than in config because that package is a leaf that
// everyone imports and these types must not carry an AppConfig dependency.
package turn

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"
)

type Session interface {
	Send(context.Context, string) (AgentResponse, error)
}

type PromptHistory interface {
	Append(context.Context, string) error
	Recent(context.Context, int) ([]string, error)
}

type IdentifiedSession interface {
	Session
	SessionID() string
}

type TranscriptMessage struct {
	Role, Text        string
	ExecutionEvidence []ExecutionEvidence
	// ToolCalls is the persisted outcome ledger for the turn (ADR 0001).
	ToolCalls     []ToolOutcome
	ToolCallCount int
	// Interrupted marks a turn stopped before it answered.
	Interrupted bool
}

type TranscriptResumer interface {
	ResumeTranscript([]TranscriptMessage) error
}

type AgentResponse struct {
	Message           string              `json:"message"`
	ExecutionEvidence []ExecutionEvidence `json:"execution_evidence"`
	ToolFlagged       []ExecutionEvidence `json:"tool_flagged,omitempty"`
	// Model is the model the run asked the provider for, and ServedModel the
	// one the provider reported serving. They are recorded per response rather
	// than read back off config, which a /model switch or a router profile can
	// disagree with.
	Model         string        `json:"model,omitempty"`
	ServedModel   string        `json:"served_model,omitempty"`
	Usage         TokenUsage    `json:"usage"`
	ToolFailures  []ToolOutcome `json:"tool_failures,omitempty"`
	ToolCallCount int           `json:"tool_call_count"`
	// StopReason is empty for a run that finished on its own answer, and names
	// the budget that ran out otherwise ("step_limit", ...). The ledger is the
	// source of truth for what happened (ADR 0001): a best-effort answer reads
	// as a confident completion, so the reason must be machine-readable and
	// not only prose.
	StopReason string `json:"stop_reason,omitempty"`
	// Retries counts the recoveries the run needed, by reason
	// ("reasoning_overflow"), so a report can tell a clean run from one that
	// only finished after the gateway rescued it.
	Retries map[string]int `json:"retries,omitempty"`
}

type ToolOutcome struct {
	Tool   string `json:"tool"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

const (
	OutcomeKindError      = "error"
	OutcomeKindDenied     = "denied"
	OutcomeKindParseError = "parse-error"
	OutcomeKindCanceled   = "canceled"
)

func AppendToolOutcomeGuard(message string, failures []ToolOutcome) string {
	if len(failures) == 0 {
		return message
	}
	var out strings.Builder
	out.WriteString(strings.TrimSpace(message))
	if out.Len() > 0 {
		out.WriteString("\n\n")
	}
	out.WriteString("[Tool attempt failures — history, not final task status]\n")
	for _, failure := range failures {
		// The first line says what failed; the rest is usually format help
		// (apply_patch's template) that reads as content when replayed.
		detail, _, _ := strings.Cut(failure.Detail, "\n")
		fmt.Fprintf(&out, "- %s %s: %s\n", toolOutcomeName(failure.Tool), failure.Kind, detail)
	}
	return strings.TrimSpace(out.String())
}

func toolOutcomeName(name string) string { return cmp.Or(strings.TrimSpace(name), "unknown") }

type tokenUsageState struct {
	mu                                                                                                               sync.Mutex
	contextWindowTokens, sessionPromptTokens, sessionCachedPromptTokens, sessionCompletionTokens, sessionTotalTokens int64
	sessionReasoningTokens                                                                                           int64
}

type TokenUsage struct {
	Available              bool  `json:"available"`
	ContextWindowTokens    int64 `json:"context_window_tokens,omitempty"`
	LatestPromptTokens     int64 `json:"latest_prompt_tokens,omitempty"`
	LatestCompletionTokens int64 `json:"latest_completion_tokens,omitempty"`
	// LatestCachedPromptTokens/LatestReasoningTokens are provider-reported and
	// zero when the provider omits them. Cached tokens are the cache-served
	// subset of the prompt, never an addition to it.
	LatestCachedPromptTokens  int64 `json:"latest_cached_prompt_tokens,omitempty"`
	LatestReasoningTokens     int64 `json:"latest_reasoning_tokens,omitempty"`
	TurnPromptTokens          int64 `json:"turn_prompt_tokens,omitempty"`
	TurnCachedPromptTokens    int64 `json:"turn_cached_prompt_tokens,omitempty"`
	TurnCompletionTokens      int64 `json:"turn_completion_tokens,omitempty"`
	TurnTotalTokens           int64 `json:"turn_total_tokens,omitempty"`
	SessionPromptTokens       int64 `json:"session_prompt_tokens,omitempty"`
	SessionCachedPromptTokens int64 `json:"session_cached_prompt_tokens,omitempty"`
	SessionCompletionTokens   int64 `json:"session_completion_tokens,omitempty"`
	SessionTotalTokens        int64 `json:"session_total_tokens,omitempty"`
	// TurnReasoningTokens sums the turn's provider-reported reasoning.
	TurnReasoningTokens int64 `json:"turn_reasoning_tokens,omitempty"`
	// SessionReasoningTokens accumulates LatestReasoningTokens alongside the other
	// session counters; it stays zero for a provider that reports no reasoning
	// count, which is why /usage falls back to a char estimate of the thinking
	// text the transcript carries.
	SessionReasoningTokens int64 `json:"session_reasoning_tokens,omitempty"`
	// SessionCost is the priced session spend, set by the runner when
	// provider pricing is configured; zero otherwise.
	SessionCost float64          `json:"session_cost,omitempty"`
	state       *tokenUsageState `json:"-"`
}

// SessionTotalsSoFar returns the session prompt/cached-prompt/completion tokens
// recorded so far plus the current turn's not-yet-recorded delta, so in-flight
// runs can be measured against a session budget. All are zero when the provider
// reports no usage.
func (u TokenUsage) SessionTotalsSoFar() (prompt, cached, completion int64) {
	prompt, cached, completion = u.TurnPromptTokens, u.TurnCachedPromptTokens, u.TurnCompletionTokens
	if u.state == nil {
		return prompt, cached, completion
	}
	u.state.mu.Lock()
	defer u.state.mu.Unlock()
	return prompt + u.state.sessionPromptTokens, cached + u.state.sessionCachedPromptTokens, completion + u.state.sessionCompletionTokens
}

func NewSessionTokenUsage(contextWindowTokens int64) TokenUsage {
	return TokenUsage{state: &tokenUsageState{contextWindowTokens: contextWindowTokens}}
}

func (u TokenUsage) WithContextWindowTokens(tokens int64) TokenUsage {
	if u.state == nil || tokens < 0 {
		return u
	}
	u.state.mu.Lock()
	u.state.contextWindowTokens = tokens
	u.state.mu.Unlock()
	u.ContextWindowTokens = tokens
	return u
}

func (u TokenUsage) NewTurnUsage() TokenUsage {
	if u.state != nil {
		u.state.mu.Lock()
		u.ContextWindowTokens = u.state.contextWindowTokens
		u.state.mu.Unlock()
	}
	return TokenUsage{
		ContextWindowTokens: u.ContextWindowTokens, state: u.state,
	}
}

func (u TokenUsage) AccumulateTurnUsage(delta TokenUsage) TokenUsage {
	u.state = cmp.Or(u.state, delta.state)
	if !delta.Available {
		return u
	}
	u.Available, u.LatestPromptTokens, u.LatestCompletionTokens = true, delta.LatestPromptTokens, delta.LatestCompletionTokens
	u.LatestCachedPromptTokens, u.LatestReasoningTokens = delta.LatestCachedPromptTokens, delta.LatestReasoningTokens
	u.TurnPromptTokens += delta.TurnPromptTokens
	u.TurnCachedPromptTokens += delta.LatestCachedPromptTokens
	u.TurnCompletionTokens += delta.TurnCompletionTokens
	u.TurnTotalTokens += delta.TurnTotalTokens
	u.TurnReasoningTokens += delta.LatestReasoningTokens
	return u
}

func (u TokenUsage) RecordUsage() TokenUsage {
	if u.state == nil {
		return u
	}
	u.state.mu.Lock()
	defer u.state.mu.Unlock()
	u.ContextWindowTokens = u.state.contextWindowTokens
	if !u.Available {
		return u
	}
	u.state.sessionPromptTokens += u.TurnPromptTokens
	u.state.sessionCachedPromptTokens += u.TurnCachedPromptTokens
	u.state.sessionCompletionTokens += u.TurnCompletionTokens
	u.state.sessionTotalTokens += u.TurnTotalTokens
	u.state.sessionReasoningTokens += u.TurnReasoningTokens
	u.SessionPromptTokens = u.state.sessionPromptTokens
	u.SessionCachedPromptTokens = u.state.sessionCachedPromptTokens
	u.SessionCompletionTokens = u.state.sessionCompletionTokens
	u.SessionTotalTokens = u.state.sessionTotalTokens
	u.SessionReasoningTokens = u.state.sessionReasoningTokens
	return u
}
