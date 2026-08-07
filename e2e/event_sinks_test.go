//go:build e2e

package e2e

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestEventSinks characterizes which event types the product actually surfaces.
//
// The bus carries fourteen event types. This case drives one real turn twice —
// once in-process with a probe handler attached to the bus, once through
// `exec --json` — and compares what the bus emits against what any product
// surface (the JSONL stream, the log, the memory dir) ever shows a user.
//
// The gap it pins down: `audit` and the four memory/prompt lifecycle families
// are emitted with fully-populated payloads and consumed by nothing. Sensitive
// tools each build an audit payload (paths, policy tier, outcome, hashed query,
// duration) on every invocation and it is dropped on the floor —
// docs/security-model.md says "every use emits an audit event", which is true,
// and yet nothing records it.
//
// If you add a sink, this test fails: shrink wantUnconsumed and update
// docs/onboarding/08-events.md, which still lists these consumers as
// "(observability)" and "(log/ledger)".
func TestEventSinks(t *testing.T) {
	// Every event family that fires during a plain tool-using turn but reaches
	// no product surface.
	wantUnconsumed := []string{
		"after_recall",
		"after_save",
		"audit",
		"before_prompt",
		"after_prompt",
		"before_recall",
		"before_save",
	}

	// One sensitive tool (audit), one memory write and one memory read (the
	// save/recall lifecycle families).
	steps := []providertest.CompletionStep{
		providertest.NativeToolCallStep("sink-search", "file_search", map[string]any{"query": "amber-orbit"}),
		providertest.NativeToolCallStep("sink-save", "memory", map[string]any{
			"action": "save", "scope": "project", "kind": "fact", "content": "Codename is amber-orbit.",
		}),
		providertest.NativeToolCallStep("sink-recall", "recall", map[string]any{"query": "codename", "limit": 3}),
		providertest.ReplyStep("done"),
	}

	provider := providertest.NewScripted(t, steps...)
	defer provider.Close()
	home := newHome(t, provider.URL())

	var mu sync.Mutex
	seen := map[string]int{}
	audits := 0
	auditDetails := map[string]any{}
	got := runInProcWhen(t, home, provider.URL(), "find the codename", grantAll, func(s runtime.Services) {
		s.Events.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
			mu.Lock()
			defer mu.Unlock()
			seen[string(ev.Type)]++
			if ev.Type == events.EventAudit && ev.Audit != nil {
				audits++
				if ev.Audit.Action == "file_search" {
					auditDetails = ev.Audit.Details
				}
			}
			return nil
		}))
	})
	if got.Resp.ToolCallCount != 3 {
		t.Fatalf("expected three tool calls, got %d", got.Resp.ToolCallCount)
	}

	// 1. The audit payload is real work, not an empty shell.
	mu.Lock()
	emitted := make([]string, 0, len(seen))
	for name := range seen {
		emitted = append(emitted, name)
	}
	auditCount, details := audits, auditDetails
	mu.Unlock()
	sort.Strings(emitted)
	if auditCount == 0 {
		t.Fatal("no audit events emitted; the case can no longer prove anything")
	}
	for _, key := range []string{"policy_tier", "query_hash", "result_count", "duration_ms"} {
		if _, ok := details[key]; !ok {
			t.Fatalf("file_search audit details missing %q: %#v", key, details)
		}
	}
	t.Logf("event types emitted on one turn: %s", strings.Join(emitted, ", "))

	// 2. Nothing a user can see carries them. Same scenario, real CLI.
	provider2 := providertest.NewScripted(t, steps...)
	defer provider2.Close()
	home2 := newHome(t, provider2.URL())
	result := runExec(t, home2, provider2.URL(), "find the codename")
	led := parseLedger(t, result.Stdout)

	surfaced := map[string]bool{}
	for _, event := range led.Events {
		if kind, ok := event["type"].(string); ok {
			surfaced[kind] = true
		}
	}
	streamed := strings.ToLower(result.Stdout + result.Stderr)

	// The audit trail is not on disk either: no file under the home or the
	// workspace carries the audit-only detail key.
	onDisk := fileTreeContains(home2.state.HomeDir, "policy_tier") || fileTreeContains(home2.workspace, "policy_tier")
	if onDisk {
		t.Fatal("found policy_tier on disk: an audit sink now exists, update wantUnconsumed and 08-events.md")
	}

	for _, family := range wantUnconsumed {
		switch {
		case !seenAny(seen, family):
			t.Fatalf("%s never fired on the probed turn; the case is stale", family)
		case surfaced[family] || strings.Contains(streamed, family):
			t.Fatalf("%s now reaches a product surface; shrink wantUnconsumed and update 08-events.md", family)
		}
	}
	t.Logf("emitted but consumed by nothing: %s", strings.Join(wantUnconsumed, ", "))
}

func seenAny(seen map[string]int, family string) bool {
	return seen[family] > 0
}
