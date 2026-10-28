package agentui

import (
	"cmp"
	"strings"

	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/platform/turn"
)

// reasoningSession is the slice of agent.Session the /reasoning command and
// the status line need.
type reasoningSession interface {
	SetReasoningEffort(string) error
	ReasoningEffort() clients.ReasoningState
}

const reasoningUnsupported = "chat template ignores reasoning_effort"

// reasoningFeedback runs /reasoning: no args shows the state, one arg sets it.
func reasoningFeedback(session turn.Session, args []string) string {
	rs, ok := session.(reasoningSession)
	if !ok {
		return "reasoning effort unavailable in this runtime"
	}
	if len(args) > 1 {
		return findCommand("reasoning").usage
	}
	if len(args) == 1 {
		if err := rs.SetReasoningEffort(args[0]); err != nil {
			return err.Error()
		}
	}
	state := rs.ReasoningEffort()
	var b strings.Builder
	b.WriteString("reasoning effort: " + cmp.Or(state.Level, "default"))
	if len(args) == 1 {
		b.WriteString(" (applies from next turn)")
	}
	switch {
	case state.Kind == "":
		b.WriteString("; no reasoning scale for this provider (llama.cpp, Ollama, OpenRouter, Anthropic), not sent")
	case state.Level == "":
		b.WriteString(" (" + string(state.Kind) + ": " + strings.Join(state.Kind.Levels(), "|") + ")")
	case !state.Kind.Accepts(state.Level):
		b.WriteString("; not in the " + string(state.Kind) + " scale (" + strings.Join(state.Kind.Levels(), "|") + "), not sent")
	case !state.Supported:
		b.WriteString("; " + reasoningUnsupported)
	}
	return b.String()
}

// reasoningLabel is the status-line cell: empty unless the provider has a known
// effort scale, in which case it names the level in force — "default" while the
// user has set none, since the provider then picks. "⚠" marks a level that will
// not take effect.
func reasoningLabel(session turn.Session) string {
	rs, ok := session.(reasoningSession)
	if !ok {
		return ""
	}
	state := rs.ReasoningEffort()
	if state.Kind == "" {
		return ""
	}
	if state.Level == "" {
		return "default"
	}
	if !state.Supported {
		return state.Level + " ⚠"
	}
	return state.Level
}
