// Package summary holds the model-agnostic memory summary domain types and the
// prompt rendering / parsing used by Invokr's memory runtime. It is shared by
// the zerfoo model worker and the memory manager so neither depends on a
// specific model runtime.
package summary

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/support/strutil"
)

// PromptHeader instructs the summarization model to produce a faithful, concise,
// JSON-only memory summary. It deliberately forbids invented facts and asks the
// model to drop transient chatter while preserving durable preferences, names,
// decisions, plans, constraints, and corrections.
const PromptHeader = `You summarize memories for a local AI agent.

Return only valid JSON with this shape:
{"summary":"short durable summary","facts":[{"subject":"user|agent|project|other","predicate":"short relation","object":"fact text","confidence":0.0}],"topics":["topic"],"importance":0.0,"should_store":true}

Rules:
- Preserve durable preferences, names, decisions, plans, constraints, tasks, and corrections.
- Drop transient filler.
- Do not invent facts; only use information present in the events.
- Prefer concise summaries.
- Set should_store=false for content that is temporary or not useful later.
`

type MemoryEvent struct {
	SessionID string            `json:"session_id,omitempty"`
	Role      string            `json:"role,omitempty"`
	Content   string            `json:"content,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type MemorySummary struct {
	Summary     string       `json:"summary"`
	Facts       []MemoryFact `json:"facts"`
	Topics      []string     `json:"topics"`
	Importance  float64      `json:"importance"`
	ShouldStore bool         `json:"should_store"`
}

type MemoryFact struct {
	Subject    string  `json:"subject"`
	Predicate  string  `json:"predicate"`
	Object     string  `json:"object"`
	Confidence float64 `json:"confidence"`
}

type StructuredSummaryInput struct {
	Events []MemoryEvent `json:"events"`
}

func RenderSummaryPrompt(input StructuredSummaryInput) (string, error) {
	if len(input.Events) == 0 {
		return "", fmt.Errorf("summary events are required")
	}
	payload, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return "", err
	}
	return PromptHeader + "\nEvents:\n" + string(payload), nil
}

func ParseMemorySummary(raw string) (MemorySummary, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return MemorySummary{}, fmt.Errorf("memory summary JSON is required")
	}
	// Models often wrap JSON in prose or fences; extract the first JSON object.
	raw = strutil.ExtractJSONObject(raw)
	var summary MemorySummary
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&summary); err != nil {
		return MemorySummary{}, fmt.Errorf("parse memory summary: %w", err)
	}
	summary.Summary = strings.TrimSpace(summary.Summary)
	if summary.Summary == "" {
		return MemorySummary{}, fmt.Errorf("memory summary text is required")
	}
	summary.Importance = mathutil.Clamp01(summary.Importance)
	if len(summary.Topics) > 8 {
		summary.Topics = summary.Topics[:8]
	}
	for i, topic := range summary.Topics {
		summary.Topics[i] = strings.TrimSpace(topic)
	}
	summary.Topics = slices.DeleteFunc(summary.Topics, func(topic string) bool { return topic == "" })
	for i := range summary.Facts {
		fact := &summary.Facts[i]
		fact.Subject = strings.TrimSpace(fact.Subject)
		fact.Predicate = strings.TrimSpace(fact.Predicate)
		fact.Object = strings.TrimSpace(fact.Object)
		fact.Confidence = mathutil.Clamp01(fact.Confidence)
	}
	summary.Facts = slices.DeleteFunc(summary.Facts, func(fact MemoryFact) bool {
		return fact.Subject == "" && fact.Predicate == "" && fact.Object == ""
	})
	return summary, nil
}

func RenderMemoryForEmbedding(summary MemorySummary) string {
	var out strings.Builder
	out.WriteString("Summary: ")
	out.WriteString(strings.TrimSpace(summary.Summary))
	if len(summary.Facts) > 0 {
		out.WriteString("\nFacts:")
		for _, fact := range summary.Facts {
			line := strings.TrimSpace(strings.Join([]string{fact.Subject, fact.Predicate, fact.Object}, " "))
			if line == "" {
				continue
			}
			out.WriteString("\n- ")
			out.WriteString(line)
		}
	}
	if len(summary.Topics) > 0 {
		out.WriteString("\nTopics: ")
		out.WriteString(strings.Join(summary.Topics, ", "))
	}
	return strings.TrimSpace(out.String())
}
