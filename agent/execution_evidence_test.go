package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/sessionstore"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools/core"
)

func TestFlaggedCompletionHasEvidenceWithoutHardFailureOrExtraTurn(t *testing.T) {
	runner := testRunner(t, func(context.Context, *core.ToolCall) (string, error) {
		return "failed check", &core.ToolError{Code: "exit 1", Payload: "failed check"}
	}, []string{`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`, `{"type":"reply","message":"All tests pass"}`})
	var persisted []turn.ExecutionEvidence
	runner.events.(*events.OrderedDispatcher).Register(events.HandlerFunc(func(_ context.Context, event events.Event) error {
		if event.Response != nil {
			persisted = event.Response.ExecutionEvidence
		}
		return nil
	}))
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil || len(resp.ToolFailures) != 0 || len(resp.ToolFlagged) != 1 || len(persisted) != 1 {
		t.Fatalf("flagged completion lost or became hard failure: %#v, persisted=%#v err=%v", resp, persisted, err)
	}
	if runner.modelGateway.(*sequenceGateway).next != 2 || resp.Message != "All tests pass" {
		t.Fatal("evidence spent another model turn or changed model text")
	}
	item := turn.TranscriptMessage{Text: resp.Message, ExecutionEvidence: resp.ExecutionEvidence}
	if replay := resumedAssistantText(item); !strings.Contains(replay, "historical") || !strings.Contains(replay, "flagged") {
		t.Fatalf("resume lost historical evidence: %s", replay)
	}
	if resp.ExecutionEvidence[0].Freshness != "current" {
		t.Fatal("resume changed original evidence")
	}
}

func TestExecutionEvidenceDiskResumeIsHistoricalAndBounded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := sessionstore.New(dir)
	evidence := make([]turn.ExecutionEvidence, 1000)
	for i := range evidence {
		evidence[i] = turn.ExecutionEvidence{Tool: "shell", Outcome: "flagged", Command: strings.Repeat("x", 2000), ExitCode: new(1), Freshness: "current"}
	}
	if _, err := store.AppendTurn(ctx, "s1", "assistant", "Tests pass", sessionstore.ToolRecord{Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "s1", "assistant", "legacy"); err != nil {
		t.Fatal(err)
	}
	loaded, err := sessionstore.New(dir).Load(ctx, "s1")
	if err != nil || len(loaded) != 2 {
		t.Fatalf("load: %v %v", loaded, err)
	}
	got := loaded[0].ExecutionEvidence
	if len(got) != 1000 || got[0].ExitCode == nil || *got[0].ExitCode != 1 || got[0].Freshness != "current" {
		t.Fatal("disk lost evidence")
	}
	replay := resumedAssistantText(turn.TranscriptMessage{Text: loaded[0].Text, ExecutionEvidence: got})
	if len(replay) > 600 || !strings.Contains(replay, "historical") || !strings.Contains(replay, "1000 flagged") {
		t.Fatalf("unbounded or missing historical evidence: %.700s", replay)
	}
	if got[0].Freshness != "current" || loaded[1].ExecutionEvidence != nil || resumedAssistantText(turn.TranscriptMessage{Text: loaded[1].Text}) != "legacy" {
		t.Fatal("resume mutated facts or legacy text")
	}

}
