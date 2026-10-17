package agent

import (
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
)

// The model's reasoning must survive into the completion so the history
// message can carry it back on the next request (#577).
func TestCompletionKeepsReasoning(t *testing.T) {
	got := completionFromResponse(&inference.Response{Message: inference.Message{Content: "done", Reasoning: "read store.go first"}})
	if got.reasoning != "read store.go first" {
		t.Fatalf("reasoning = %q", got.reasoning)
	}
	got = completionFromResponse(&inference.Response{Reasoning: inference.Reasoning{Summary: "summary only"}})
	if got.reasoning != "summary only" {
		t.Fatalf("fallback to Reasoning.Summary: got %q", got.reasoning)
	}
}
