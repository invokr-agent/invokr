package cmd

import (
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
