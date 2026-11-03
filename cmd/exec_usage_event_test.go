package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/cmd/execwire"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

type usageSink struct{ buf strings.Builder }

func (w *usageSink) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *usageSink) text() string                { return w.buf.String() }

func TestExecJSONLUsageEvent(t *testing.T) {
	event := events.Event{Type: events.EventUsage, SessionID: "s1",
		Usage: &turn.TokenUsage{Available: true, ContextWindowTokens: 32_000, LatestPromptTokens: 12_000,
			LatestCompletionTokens: 1_000}}
	out, errOut := &usageSink{}, &usageSink{}
	if err := writeExecEvent(out, event); err != nil {
		t.Fatalf("writeExecEvent: %v", err)
	}
	line := strings.TrimSpace(out.text())
	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("line is not one JSON object: %q (%v)", line, err)
	}
	if parsed[execwire.KeyType] != execwire.TypeUsage {
		t.Fatalf("type = %v, want %q", parsed[execwire.KeyType], execwire.TypeUsage)
	}
	if parsed[execwire.KeyUsage] == nil {
		t.Fatalf("usage payload missing: %q", line)
	}

	out, errOut = &usageSink{}, &usageSink{}
	if err := handleExecEvent(out, errOut, event, false); err != nil {
		t.Fatalf("handleExecEvent: %v", err)
	}
	if out.text() != "" || errOut.text() != "" {
		t.Fatalf("non-JSON exec printed %q / %q, want nothing", out.text(), errOut.text())
	}
}

// TestExecJSONLExcludesChildSessionUsage records the decision for exec: a
// delegated subagent runs in its own session, and the run does not own that
// session, so its usage events stay out of the parent's ledger - the same call
// already made for tool events.
func TestExecJSONLExcludesChildSessionUsage(t *testing.T) {
	bus := events.NewOrderedDispatcher(nil)
	var out bytes.Buffer
	stop := registerExecEvents(bus, &out, &bytes.Buffer{}, "parent", true)
	defer stop()

	child := events.Event{Type: events.EventUsage, SessionID: "child", ParentSessionID: "parent",
		Usage: &turn.TokenUsage{Available: true, ContextWindowTokens: 32_000, LatestPromptTokens: 30_000}}
	own := events.Event{Type: events.EventUsage, SessionID: "parent",
		Usage: &turn.TokenUsage{Available: true, ContextWindowTokens: 32_000, LatestPromptTokens: 1_000}}
	bus.Dispatch(context.Background(), child)
	bus.Dispatch(context.Background(), own)
	if got := strings.Count(out.String(), "\n"); got != 1 {
		t.Fatalf("JSONL lines = %d, want only the run's own usage: %q", got, out.String())
	}
}
