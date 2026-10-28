package recallgate

import (
	"encoding/json"
	"fmt"
	"github.com/invokr-agent/invokr/support/strutil"
	"strings"

	"github.com/invokr-agent/invokr/config"
)

// promptHeader instructs the probe model to act as a memory-recall router and
// answer with JSON only. The user message and the (optional) nothink suffix are
// appended by renderPrompt.
const promptHeader = `You are a memory-recall router for an agent. Given the user's request,
decide whether searching the agent's long-term memory (past conversations,
stored facts, project notes) would materially help. Most requests do not
need recall. Answer with JSON only:
{"recall": <bool>, "queries": [<up to N short search strings>]}
User request:
`

const maxQueryChars = 120

// Decision is the parsed probe result: whether recall would help and, if so,
// the search strings to run.
type Decision struct {
	Recall  bool     `json:"recall"`
	Queries []string `json:"queries"`
}

// renderPrompt builds the single-user-message probe prompt for the given user
// message. A non-empty nothinkSuffix rides in the prompt text on its own line.
func renderPrompt(userMessage, nothinkSuffix string) string {
	var b strings.Builder
	b.WriteString(promptHeader)
	b.WriteString(strings.TrimSpace(userMessage))
	if suffix := strings.TrimSpace(nothinkSuffix); suffix != "" {
		b.WriteString("\n")
		b.WriteString(suffix)
	}
	return b.String()
}

// parseDecision forgivingly parses a probe completion: it strips any leading
// <think>…</think> block and code fences, extracts the first JSON object, and
// clamps the queries. Any failure returns Decision{Recall: false} with an error
// so the caller can log it; callers must degrade to "no recall" on error.
func parseDecision(raw string, cfg config.RecallGateConfig, userMessage string) (Decision, error) {
	raw = stripThink(strings.TrimSpace(raw))
	if raw == "" {
		return Decision{}, fmt.Errorf("recall gate: empty probe response")
	}
	raw = strutil.ExtractJSONObject(raw)
	var decision Decision
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&decision); err != nil {
		return Decision{}, fmt.Errorf("recall gate: parse probe decision: %w", err)
	}
	if !decision.Recall {
		return Decision{Recall: false}, nil
	}
	decision.Queries = clampQueries(decision.Queries, cfg.MaxQueries)
	if len(decision.Queries) == 0 {
		if msg := strings.TrimSpace(userMessage); msg != "" {
			decision.Queries = []string{strutil.TruncateUTF8(msg, maxQueryChars)}
		}
	}
	return decision, nil
}

// clampQueries drops blank queries, truncates each to maxQueryChars, and caps
// the list at maxQueries.
func clampQueries(queries []string, maxQueries int) []string {
	out := queries[:0]
	for _, q := range queries {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		out = append(out, strutil.TruncateUTF8(q, maxQueryChars))
		if len(out) >= maxQueries {
			break
		}
	}
	return out
}

// stripThink removes a single leading <think>…</think> block emitted by models
// that ignore the nothink suffix.
func stripThink(raw string) string {
	const openTag, closeTag = "<think>", "</think>"
	if !strings.HasPrefix(raw, openTag) {
		return raw
	}
	if end := strings.Index(raw, closeTag); end >= 0 {
		return strings.TrimSpace(raw[end+len(closeTag):])
	}
	return raw
}
