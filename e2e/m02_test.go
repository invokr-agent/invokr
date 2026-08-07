//go:build e2e

package e2e

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/agentprompt"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// assertNoOrphanedToolCalls fails when a request carries a tool result whose
// tool_call_id no earlier assistant message announced. An OpenAI-shaped
// provider rejects such a request with a 400, which is the failure mode ADR
// 0013's pair-safe retention exists to prevent.
func assertNoOrphanedToolCalls(t *testing.T, requests []providertest.CompletionRequest) {
	t.Helper()
	for reqIndex, req := range requests {
		announced := map[string]bool{}
		for _, message := range req.Messages {
			if message.ToolCalls != nil {
				encoded, err := json.Marshal(message.ToolCalls)
				if err != nil {
					t.Fatal(err)
				}
				var calls []struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(encoded, &calls); err != nil {
					t.Fatalf("decode tool_calls: %v", err)
				}
				for _, call := range calls {
					announced[call.ID] = true
				}
			}
			if message.Role == "tool" && !announced[message.ToolCallID] {
				t.Fatalf("request %d carries orphaned tool_call_id %q", reqIndex+1, message.ToolCallID)
			}
		}
	}
}

// TestM02_PairSafeRetention covers plan case M02: after compaction the newest
// workspace-write result survives verbatim (ADR 0013's "never drop the last
// edit"), the summary is present, and no request ever ships an orphaned
// tool_call_id. Before the fix pinning was honoured only by the elision
// fallback, so the summarize path — the one that normally runs — dropped it.
func TestM02_PairSafeRetention(t *testing.T) {
	const (
		canary  = "PINNED-CANARY-77"
		summary = "m02-marked-summary"
	)
	home := newHome(t, "")
	home.app.Provider.ContextWindowTokens = 3_700 // shorter system prompt: compact after the third read
	// Exercise summarization independently of bench defaults.
	home.app.Agent.ContextWarningPercent = 70
	home.app.Agent.ContextCompactPercent = 80
	for i := 1; i <= 3; i++ {
		mustWrite(t, filepath.Join(home.workspace, "m02-large-"+string(rune('0'+i))+".txt"), strings.Repeat(string(rune('a'+i)), 2_400))
	}
	steps := []providertest.CompletionStep{
		patchCall("m02-patch", "*** Begin Patch\n*** Add File: pinned.txt\n+"+canary+"\n*** End Patch"),
		providertest.NativeToolCallStep("m02-read-1", "read", map[string]any{"paths": []string{"m02-large-1.txt"}}),
		providertest.NativeToolCallStep("m02-read-2", "read", map[string]any{"paths": []string{"m02-large-2.txt"}}),
		providertest.NativeToolCallStep("m02-read-3", "read", map[string]any{"paths": []string{"m02-large-3.txt"}}),
	}
	for range 8 {
		steps = append(steps, providertest.CompletionStep{MutateForRequest: func(req providertest.CompletionRequest, step *providertest.CompletionStep) {
			if strings.Contains(requestText(req), "Summarize this quoted conversation segment compactly") {
				step.Content = summary
			} else {
				*step = providertest.ReplyStep("m02-done")
			}
		}})
	}
	provider := providertest.New(t, steps, providertest.WithContextWindow(3_700))
	defer provider.Close()

	got := runInProc(t, home, provider.URL(), "m02-original-request", grantAll)
	if got.Resp.Message != "m02-done" {
		t.Fatalf("final response = %q, want m02-done", got.Resp.Message)
	}

	requests := provider.Snapshot().CompletionRequests
	assertNoOrphanedToolCalls(t, requests)

	var compacted int
	for _, req := range requests {
		text := requestText(req)
		if !strings.Contains(text, "assistant:"+agentprompt.SummaryLabel+strconv.Quote(summary)) {
			continue
		}
		compacted++
		if !strings.Contains(text, "m02-original-request") {
			t.Fatal("compacted request lost the original user request")
		}
		encoded, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		// The apply_patch result is the newest workspace write, so it must
		// survive summarization verbatim.
		if !strings.Contains(string(encoded), canary) {
			t.Fatalf("compaction dropped the pinned apply_patch result:\n%s", text)
		}
	}
	if compacted == 0 {
		t.Fatal("no request carried the conversation summary; compaction never ran")
	}
	provider.AssertClean(t)
}
