//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/memory/markdown"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// x04RecallMarker is the fact seeded into project memory; the recall gate's
// query matches it, so the gate block renders with real content.
const x04RecallMarker = "x04-recall-marker-91"

// x04GateBlockTitle heads the recall gate's block (recall.HitsPromptBlock). The
// gate runs on the first turn only, so this string is the per-turn-varying part
// of the prompt: present in turn 1, absent from turn 2.
const x04GateBlockTitle = "## Relevant Memory"

// x04GateProbe is the recall gate's scripted probe answer: recall is warranted,
// with a single simple-term query that matches the seeded project fact.
const x04GateProbe = `{"recall": true, "queries": ["codename"]}`

// cacheStep is one scripted reply that reports OpenAI-style usage with a
// cache-served prompt share.
func cacheStep(message string) providertest.CompletionStep {
	step := providertest.ReplyStep(message)
	step.Usage = &providertest.StepUsage{PromptTokens: 1000, CompletionTokens: 100, CachedTokens: 800}
	return step
}

// agentTurns drops the summarizer's one-shot calls (a single message, no tools)
// so only the agent's own completion bodies are compared.
func agentTurns(requests []providertest.CompletionRequest) []providertest.CompletionRequest {
	var out []providertest.CompletionRequest
	for _, req := range requests {
		if len(req.Messages) > 1 {
			out = append(out, req)
		}
	}
	return out
}

// prefixMismatch describes where next stops being a byte-identical
// prefix-extension of prev's transcript — the property a provider prompt cache
// needs to hit — or "" when it is one.
func prefixMismatch(t *testing.T, prev, next providertest.CompletionRequest) string {
	t.Helper()
	if len(next.Messages) < len(prev.Messages) {
		return fmt.Sprintf("request shrank from %d to %d messages", len(prev.Messages), len(next.Messages))
	}
	for i, want := range prev.Messages {
		if got, wantJSON := mustJSON(t, next.Messages[i]), mustJSON(t, want); got != wantJSON {
			return fmt.Sprintf("message %d churned\nprev: %s\nnext: %s", i, wantJSON, got)
		}
	}
	return ""
}

// assertPrefixExtension additionally requires the tool definitions to match,
// since they are serialized ahead of the transcript.
func assertPrefixExtension(t *testing.T, label string, prev, next providertest.CompletionRequest) {
	t.Helper()
	if prevTools, nextTools := mustJSON(t, prev.Tools), mustJSON(t, next.Tools); prevTools != nextTools {
		t.Fatalf("%s: tool definitions churned\nprev: %s\nnext: %s", label, prevTools, nextTools)
	}
	if mismatch := prefixMismatch(t, prev, next); mismatch != "" {
		t.Fatalf("%s: %s", label, mismatch)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// TestX04_PromptCachePrefixStability covers issue #359's cache-friendly request
// shaping and usage accounting end to end: three user turns in one session must
// each send a byte-identical prefix-extension of the previous request (system
// prompt, tool definitions, and prior history unchanged in content and order),
// the provider's prompt_tokens_details.cached_tokens must reach the session
// usage the ledger reports, and an explicit compaction must rewrite the prefix
// once and then establish a new stable one.
func TestX04_PromptCachePrefixStability(t *testing.T) {
	// The first step answers the recall gate's probe; the rest are agent turns
	// plus the one compaction summary.
	// The gate's probe is a plain JSON completion, not a tagged-JSON agent
	// reply, so it is scripted as raw content.
	steps := []providertest.CompletionStep{{Content: x04GateProbe}}
	for _, msg := range []string{"one", "two", "three", "summary", "four", "five"} {
		steps = append(steps, cacheStep(msg))
	}
	provider := providertest.New(t, steps, providertest.WithContextWindow(4000))
	defer provider.Close()
	home := newHome(t, provider.URL())
	// Seed project memory and arm the first-turn recall gate. Without this the
	// case is vacuous: with nothing recalled, every turn renders an identical
	// memory section and even a system-prompt-resident block stays byte-stable.
	// The gate fires on turn 1 only, so the recall block is present in turn 1's
	// prompt and absent from turn 2's - exactly the prefix churn issue #359 is
	// about, and exactly what moving the block out of the system message fixes.
	mustWrite(t, filepath.Join(markdown.ProjectDirFor(home.state.MemoryDir, home.workspace), "MEMORY.md"),
		"- Project codename amber-orbit, tracking id "+x04RecallMarker+".\n")
	home.app.Memory.RecallGate.Enabled = true
	home.app.Provider.ContextWindowTokens = 4000
	home.app.Provider.Pricing.PromptPer1M = 10
	home.app.Provider.Pricing.CachedPromptPer1M = 1
	home.app.Provider.Pricing.CompletionPer1M = 30
	home.app.Provider.BaseURL = provider.URL()

	rt, err := runtime.Bootstrap(home.app, runtime.Options{})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = rt.Cleanup() })
	services := rt.Services()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var last turn.AgentResponse
	for _, prompt := range []string{"first question", "second question", "third question"} {
		if last, err = services.Session.Send(ctx, prompt); err != nil {
			t.Fatalf("send %q: %v", prompt, err)
		}
	}

	requests := agentTurns(provider.Snapshot().CompletionRequests)
	if len(requests) < 3 {
		t.Fatalf("agent completion requests = %d, want at least 3", len(requests))
	}
	// Non-vacuity guard: turn 1 must really carry a first-turn-only recall
	// block. Without one every turn renders identical memory content and the
	// prefix assertions below would hold wherever that content lives - which is
	// what made an earlier version of this case prove nothing.
	if first := requestText(requests[0]); !strings.Contains(first, x04GateBlockTitle) || !strings.Contains(first, x04RecallMarker) {
		t.Fatalf("turn 1 carried no recall-gate block, so prefix stability proves nothing:\n%s", first)
	}
	for i := 1; i < len(requests); i++ {
		assertPrefixExtension(t, "steady state", requests[i-1], requests[i])
	}

	// Usage accounting: the provider's cached_tokens reach the session totals.
	if !last.Usage.Available || last.Usage.SessionCachedPromptTokens == 0 {
		t.Fatalf("session usage did not surface cached prompt tokens: %#v", last.Usage)
	}
	if last.Usage.SessionCachedPromptTokens >= last.Usage.SessionPromptTokens {
		t.Fatalf("cached %d must be a subset of prompt %d", last.Usage.SessionCachedPromptTokens, last.Usage.SessionPromptTokens)
	}
	// Cached tokens are billed at the cached rate, so the session costs less
	// than pricing every prompt token at the full rate would.
	full := float64(last.Usage.SessionPromptTokens)*10/1e6 + float64(last.Usage.SessionCompletionTokens)*30/1e6
	if last.Usage.SessionCost <= 0 || last.Usage.SessionCost >= full {
		t.Fatalf("session cost = %v, want a cache discount below %v", last.Usage.SessionCost, full)
	}

	// Compaction boundary: the forced rewrite is allowed to break the prefix,
	// but the turns after it must prefix-extend the new one.
	compactor, ok := services.Session.(interface{ RequestCompaction() })
	if !ok {
		t.Fatal("session cannot be asked to compact")
	}
	compactor.RequestCompaction()
	before := len(agentTurns(provider.Snapshot().CompletionRequests))
	for _, prompt := range []string{"fourth question", "fifth question"} {
		if _, err := services.Session.Send(ctx, prompt); err != nil {
			t.Fatalf("send %q: %v", prompt, err)
		}
	}
	after := agentTurns(provider.Snapshot().CompletionRequests)
	if len(after) < before+2 {
		t.Fatalf("post-compaction requests = %d, want at least %d", len(after), before+2)
	}
	if prefixMismatch(t, after[before-1], after[before]) == "" {
		t.Fatal("forced compaction did not rewrite the transcript, so the boundary case proves nothing")
	}
	for i := before + 1; i < len(after); i++ {
		assertPrefixExtension(t, "post-compaction", after[i-1], after[i])
	}
	provider.AssertClean(t)
}
