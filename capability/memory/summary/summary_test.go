package summary

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSummaryPromptIncludesEvents(t *testing.T) {
	prompt, err := RenderSummaryPrompt(StructuredSummaryInput{Events: []MemoryEvent{{
		SessionID: "s1",
		Role:      "user",
		Content:   "Remember I prefer terse plans.",
		CreatedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
	}}})
	if err != nil {
		t.Fatalf("RenderSummaryPrompt returned error: %v", err)
	}
	for _, want := range []string{"Return only valid JSON", "Do not invent facts", "Remember I prefer terse plans.", `"session_id": "s1"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestRenderSummaryPromptRequiresEvents(t *testing.T) {
	if _, err := RenderSummaryPrompt(StructuredSummaryInput{}); err == nil {
		t.Fatal("expected error for empty events")
	}
}

func TestParseMemorySummaryValidatesAndClamps(t *testing.T) {
	raw := `{"summary":" keep concise ","facts":[{"subject":" user ","predicate":"prefers","object":" terse plans ","confidence":2}],"topics":["go","memory","a","b","c","d","e","f","g"],"importance":-1,"should_store":true}`
	got, err := ParseMemorySummary(raw)
	if err != nil {
		t.Fatalf("ParseMemorySummary returned error: %v", err)
	}
	if got.Summary != "keep concise" {
		t.Fatalf("unexpected summary %q", got.Summary)
	}
	if got.Importance != 0 || got.Facts[0].Confidence != 1 {
		t.Fatalf("expected clamped values, got %#v", got)
	}
	if len(got.Topics) != 8 {
		t.Fatalf("expected topic cap, got %d", len(got.Topics))
	}
}

func TestParseMemorySummaryAllowsUnknownFields(t *testing.T) {
	got, err := ParseMemorySummary(`{"summary":"ok","should_store":true,"extra":1}`)
	if err != nil {
		t.Fatalf("ParseMemorySummary returned error: %v", err)
	}
	if got.Summary != "ok" || !got.ShouldStore {
		t.Fatalf("unexpected summary parse result: %#v", got)
	}
}

func TestParseMemorySummaryExtractsFromProse(t *testing.T) {
	raw := "Here is the summary:\n```json\n{\"summary\":\"ok\",\"should_store\":true}\n```\nDone."
	got, err := ParseMemorySummary(raw)
	if err != nil {
		t.Fatalf("ParseMemorySummary returned error: %v", err)
	}
	if got.Summary != "ok" {
		t.Fatalf("unexpected summary %q", got.Summary)
	}
}
