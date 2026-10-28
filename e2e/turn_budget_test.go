//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

// TestC19_RequestTokenBudgetEndsLoopOnUsagelessProvider covers the backstop that
// makes the raised turn ceiling safe: a provider that reports no usage leaves
// both session caps inert, so only the per-request token cap — enforced from a
// local estimate — bounds the loop. Streamed steps carry no usage object, which
// is exactly the llama.cpp/Ollama shape this guards against.
func TestC19_RequestTokenBudgetEndsLoopOnUsagelessProvider(t *testing.T) {
	padded := map[string]any{"query": strings.Repeat("tool_search ", 2000), "include_schema": true}
	steps := make([]providertest.CompletionStep, 0, 21)
	for range 20 {
		step := providertest.NativeToolCallStep("c19", "tool_search", padded)
		// A streamed response emits no usage object, so usage.Available stays
		// false for the whole run and the session caps cannot fire.
		step.SSEChunks = []providertest.CompletionStreamChunk{{ToolCalls: step.ToolCalls}}
		steps = append(steps, step)
	}
	steps = append(steps, providertest.ReplyStep("best effort answer"))

	provider := providertest.NewScripted(t, steps...)
	defer provider.Close()
	home := newHome(t, provider.URL())
	home.app.Agent.MaxRequestTokens = 4000

	got := runInProc(t, home, provider.URL(), "keep searching", grantAll)

	if got.Resp.Message == "" {
		t.Fatalf("want a best-effort answer after the token budget ended the loop, got empty")
	}
	completions := len(provider.Snapshot().CompletionRequests)
	if completions >= home.app.Agent.MaxTurns {
		t.Fatalf("completion requests = %d, want the token budget to bind before the %d-turn ceiling", completions, home.app.Agent.MaxTurns)
	}
}
