//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// markupOnlyReply is what a model under DisableTools emits when it still wants
// to call a tool: textual tool-call syntax and nothing else. It must never be
// rendered as the best-effort answer.
const markupOnlyReply = "<tool_call>\n<function=read_file>\n<parameter=path>README.md</parameter>\n</function>\n</tool_call>"

func assertNoToolMarkup(t *testing.T, answer string) {
	t.Helper()
	for _, marker := range []string{"<tool_call", "<|tool_call", "<function=", "<parameter="} {
		if strings.Contains(answer, marker) {
			t.Fatalf("best-effort answer leaked tool-call markup %q: %q", marker, answer)
		}
	}
}

// TestB06_GracefulExhaustionShape covers plan case B06: when a budget ends the
// loop, the final tool-free completion must yield prose — never the raw
// tool-call markup a model emits once tools are disabled. Both budget paths
// (turn ceiling and request-token backstop) go through gracefulExhaustion.
func TestB06_GracefulExhaustionShape(t *testing.T) {
	searchStep := func(id string) providertest.CompletionStep {
		return providertest.NativeToolCallStep(id, "tools", map[string]any{"action": "search", "query": "tool_search", "include_schema": true})
	}

	t.Run("turn ceiling markup-only answer falls back to the literal notice", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			searchStep("b06-a"),
			searchStep("b06-b"),
			providertest.CompletionStep{Content: markupOnlyReply},
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		home.app.Agent.MaxTurns = 2

		got := runInProc(t, home, provider.URL(), "keep searching", grantAll)

		assertNoToolMarkup(t, got.Resp.Message)
		// The salvage produced no prose, so the answer is the canned notice —
		// opened by the completed-calls note, which says the two tool calls
		// that did run may already have left work on disk.
		const want = "2 tool calls did complete first, so part of the work may already be on disk. " +
			"I wasn't able to finish: step limit reached (2 turns). Please simplify or rephrase the request."
		if got.Resp.Message != want {
			t.Fatalf("message = %q, want the ADR 0014 fallback %q", got.Resp.Message, want)
		}
		if got.Resp.StopReason != "step_limit" {
			t.Fatalf("stop reason = %q, want step_limit", got.Resp.StopReason)
		}
		// C17's invariant, re-checked on the markup path: exactly one extra
		// tool-free completion after the two tool turns.
		if n := len(provider.Snapshot().CompletionRequests); n != 3 {
			t.Fatalf("completion requests = %d, want 3 (2 tool turns + best effort)", n)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("turn ceiling keeps the prose written before the markup", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			searchStep("b06-c"),
			providertest.CompletionStep{Content: "Here is what I found so far.\n" + markupOnlyReply},
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		home.app.Agent.MaxTurns = 1

		got := runInProc(t, home, provider.URL(), "keep searching", grantAll)

		assertNoToolMarkup(t, got.Resp.Message)
		// A salvaged answer over an unfinished run opens with the step-limit
		// banner, so it cannot be read as a confident completion.
		const want = "\u26a0 step limit reached (1 turn): work may be incomplete\n\nHere is what I found so far."
		if got.Resp.Message != want {
			t.Fatalf("message = %q, want the banner and the prose prefix %q", got.Resp.Message, want)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})

	t.Run("request token cap answer has no tool markup", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			searchStep("b06-d"),
			providertest.CompletionStep{Content: markupOnlyReply},
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		home.app.Agent.MaxTurns = 20
		// One tool turn is enough to blow a 1-token request budget, so the
		// backstop — not the turn ceiling — ends this loop.
		home.app.Agent.MaxRequestTokens = 1

		got := runInProc(t, home, provider.URL(), "keep searching", grantAll)

		assertNoToolMarkup(t, got.Resp.Message)
		if !strings.Contains(got.Resp.Message, "request token budget reached") {
			t.Fatalf("message = %q, want the request-token fallback", got.Resp.Message)
		}
		if n := len(provider.Snapshot().CompletionRequests); n != 2 {
			t.Fatalf("completion requests = %d, want 2 (1 tool turn + best effort)", n)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
	})
}
