//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestM01_ElisionFallbackReduces covers plan case M01.3: when summarization
// fails, the fallback must actually elide old tool output — the notice may not
// claim a reduction that did not happen. Before the fix the trigger and the
// elision budget were measured on different bases, so nothing was ever elided
// while the notice said otherwise.
func TestM01_ElisionFallbackReduces(t *testing.T) {
	home := newHome(t, "")
	// Keep this narrow enough that the failed summary requires real elision.
	home.app.Provider.ContextWindowTokens = 3_900
	// Exercise the failed-summary path independently of bench defaults.
	home.app.Agent.ContextWarningPercent = 70
	home.app.Agent.ContextCompactPercent = 80
	for i := 1; i <= 3; i++ {
		mustWrite(t, filepath.Join(home.workspace, "m01-large-"+string(rune('0'+i))+".txt"), strings.Repeat(string(rune('a'+i)), 6_000))
	}
	// The summarizer shares this provider. An empty completion is the cheapest
	// deterministic summarizer failure ("summarize conversation: empty
	// response") — an HTTP error would be retried with backoff and would
	// consume an unpredictable number of steps.
	steps := []providertest.CompletionStep{
		providertest.NativeToolCallStep("m01-read-1", "read", map[string]any{"paths": []string{"m01-large-1.txt"}}),
		providertest.NativeToolCallStep("m01-read-2", "read", map[string]any{"paths": []string{"m01-large-2.txt"}}),
		providertest.NativeToolCallStep("m01-read-3", "read", map[string]any{"paths": []string{"m01-large-3.txt"}}),
	}
	// A failed prefetch can be retried synchronously. Runtime snapshots change
	// when a non-empty segment becomes eligible; route summary requests by
	// purpose instead of letting them consume the agent's final reply.
	for range 5 {
		steps = append(steps, providertest.CompletionStep{MutateForRequest: func(req providertest.CompletionRequest, step *providertest.CompletionStep) {
			if !strings.Contains(requestText(req), "Summarize this quoted conversation segment compactly") {
				*step = providertest.ReplyStep("m01-done")
			}
		}})
	}
	provider := providertest.New(t, steps, providertest.WithContextWindow(3_900))
	defer provider.Close()

	got := runInProc(t, home, provider.URL(), "m01-original-request", grantAll)
	provider.AssertClean(t)
	if got.Resp.Message != "m01-done" {
		t.Fatalf("final response=%q", got.Resp.Message)
	}

	if !got.hasNotice("context summarization failed; elided old tool output") {
		t.Fatalf("notices = %v, want the honest elision notice", got.Notices)
	}
	if got.hasNotice("context could not be reduced") {
		t.Fatalf("fallback reduced nothing: %v", got.Notices)
	}

	requests := provider.Snapshot().CompletionRequests
	elidedIndex := -1
	sizes := make([]int, len(requests))
	for i, req := range requests {
		text := requestText(req)
		sizes[i] = len(text)
		if elidedIndex < 0 && (strings.Contains(text, "elided to fit context]") || strings.Contains(text, "[elided read ")) {
			elidedIndex = i
		}
	}
	if elidedIndex < 0 {
		t.Fatal("no request carried elided tool output")
	}
	// The reduction is real, not just announced: the oldest read's payload was
	// carried by every earlier request and is gone from the elided one. (Raw
	// request size is not the oracle — the same turn appends a fresh tool
	// result, so a genuinely reduced transcript can still grow.)
	oldest := strings.Repeat("b", 200)
	if !strings.Contains(requestText(requests[elidedIndex-1]), oldest) {
		t.Fatal("fixture did not put the oldest read payload in the pre-elision request")
	}
	if strings.Contains(requestText(requests[elidedIndex]), oldest) {
		t.Fatalf("elision notice fired but the oldest tool output is still in the transcript (%d bytes)", sizes[elidedIndex])
	}
	assertNoOrphanedToolCalls(t, requests)
}
