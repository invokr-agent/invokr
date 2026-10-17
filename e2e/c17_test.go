//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestC17_OrphanedMarkupAndMaxTurns re-verifies, through the public runtime
// surface, that (a) an assistant reply containing raw tool-call markup with no
// real tool call is nudged rather than rendered, and (b) exhausting MaxTurns
// degrades to a best-effort answer with the ledger intact.
func TestC17_OrphanedMarkupAndMaxTurns(t *testing.T) {
	t.Run("orphaned markup nudged not rendered", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			providertest.CompletionStep{Content: "I'll read the file.\n<tool_call>\n<function=read_file>\n<parameter=path>README.md</parameter>\n</function>\n</tool_call>"},
			providertest.ReplyStep("here is the real answer"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())

		got := runInProc(t, home, provider.URL(), "read the file", grantAll)

		if got.Resp.Message != "here is the real answer" {
			t.Fatalf("message = %q, want the recovered answer", got.Resp.Message)
		}
		if strings.Contains(got.Resp.Message, "<tool_call>") || strings.Contains(got.Resp.Message, "<function=") {
			t.Fatalf("raw tool-call markup leaked into the response: %q", got.Resp.Message)
		}
		const orphanError = "reply contained literal tool-call markup that was not executed; re-issue the call using the tool-call mechanism, not text"
		failures := got.Resp.ToolFailures
		if len(failures) != 1 || failures[0].Tool != "unknown" || failures[0].Kind != "parse-error" || failures[0].Detail != orphanError {
			t.Fatalf("orphan ledger outcome = %#v", failures)
		}
		if !requestsContain(provider.Snapshot().CompletionRequests[1].Messages, orphanError) {
			t.Fatalf("runner did not send exact orphan recovery nudge")
		}
	})

	t.Run("max turns degrades to best effort", func(t *testing.T) {
		provider := providertest.NewScripted(t,
			providertest.NativeToolCallStep("c17-a", "tools", map[string]any{"action": "search", "query": "tool_search", "include_schema": true}),
			providertest.NativeToolCallStep("c17-b", "tools", map[string]any{"action": "search", "query": "tool_search", "include_schema": true}),
			providertest.ReplyStep("best effort answer"),
		)
		defer provider.Close()
		home := newHome(t, provider.URL())
		home.app.Agent.MaxTurns = 2

		got := runInProc(t, home, provider.URL(), "keep searching", grantAll)

		// The step-limit banner marks the run as incomplete; the model's own
		// best-effort prose follows it.
		if want := "\u26a0 step limit reached (2 turns): work may be incomplete\n\nbest effort answer"; got.Resp.Message != want {
			t.Fatalf("message = %q, want %q", got.Resp.Message, want)
		}
		// Two tool turns ran, then a best-effort completion was requested.
		if n := len(provider.Snapshot().CompletionRequests); n != 3 {
			t.Fatalf("completion requests = %d, want 3 (2 tool turns + best effort)", n)
		}
		if got.Resp.ToolCallCount != 2 {
			t.Fatalf("tool_call_count = %d, want 2", got.Resp.ToolCallCount)
		}
		if len(got.Resp.ToolFailures) != 0 {
			t.Fatalf("tool failures = %#v, want intact successful ledger", got.Resp.ToolFailures)
		}
		finalRequest := provider.Snapshot().CompletionRequests[2]
		var resultIDs []string
		for _, message := range finalRequest.Messages {
			if message.Role == "tool" {
				resultIDs = append(resultIDs, message.ToolCallID)
			}
		}
		if len(resultIDs) != 2 || resultIDs[0] != "c17-a" || resultIDs[1] != "c17-b" {
			t.Fatalf("best-effort request tool ledger = %v, want [c17-a c17-b]", resultIDs)
		}
	})
}
