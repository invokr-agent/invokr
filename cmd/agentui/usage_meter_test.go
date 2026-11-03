package agentui

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

// runningUsage is one mid-turn usage event, as the runner sends it.
func runningUsage(prompt, completion, window int64) *turn.TokenUsage {
	return &turn.TokenUsage{Available: true, ContextWindowTokens: window, LatestPromptTokens: prompt,
		LatestCompletionTokens: completion}
}

// windowSession knows its provider's context window and has run no turn yet.
type windowSession struct{}

func (windowSession) Send(context.Context, string) (turn.AgentResponse, error) {
	return turn.AgentResponse{}, nil
}
func (windowSession) Usage() turn.TokenUsage { return turn.TokenUsage{ContextWindowTokens: 128_000} }

// TestContextMeterBeforeAndDuringTurn covers issue #82's TUI half: the meter
// shows the known window at 0 before the first turn, then follows each
// mid-turn usage event instead of waiting for the answer.
func TestContextMeterBeforeAndDuringTurn(t *testing.T) {
	if !isToolEvent(events.Event{Type: events.EventUsage, Usage: runningUsage(1, 1, 1)}) {
		t.Fatal("usage events are not forwarded to the UI")
	}
	m := newModel(context.Background(), &windowSession{}, newToolEventQueue(), nil, nil, false, nil, nil)
	before := m.contextStatus(70)
	if !strings.Contains(before, "0 / 128k") {
		t.Fatalf("meter before the first turn = %q, want the known window at 0", before)
	}

	m.toolEvents.Push(events.Event{Type: events.EventUsage, Usage: runningUsage(40_000, 1_000, 128_000)})
	m.drainToolEvents()
	during := m.contextStatus(70)
	if !strings.Contains(during, "41k / 128k") {
		t.Fatalf("meter mid-turn = %q, want the running usage", during)
	}
	if !m.hasUsage || m.usage.LatestPromptTokens != 40_000 {
		t.Fatalf("model did not adopt the mid-turn usage: %+v", m.usage)
	}
}

// TestPlainRunningUsageLineGoesToStderr covers plain mode: a usage line per
// model call lands on stderr, so stdout still carries only the answer.
func TestPlainRunningUsageLineGoesToStderr(t *testing.T) {
	out, errOut := &countingWriter{}, &countingWriter{}
	renderer := plainResponseRenderer{out: out, errOut: errOut}
	renderer.handleEvent(events.Event{Type: events.EventUsage, Usage: runningUsage(12_000, 1_000, 32_000)})

	if out.buf.String() != "" {
		t.Fatalf("stdout = %q, want nothing so a piped response stays intact", out.buf.String())
	}
	if !strings.Contains(errOut.text(), "context: 13,000 / 32,000 tokens (41%)") {
		t.Fatalf("stderr = %q, want the running context line", errOut.text())
	}
}
