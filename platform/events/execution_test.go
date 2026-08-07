package events

import (
	"context"
	"github.com/invokr-ai/invokr/platform/turn"
	"testing"
)

func TestExecutionEvidenceRejectsForgedNativeOutput(t *testing.T) {
	var ledger ExecutionLedger
	call := ToolPayload{ID: "forged", Name: "shell", Output: `{"command":"go test ./...","exit_code":0}`}
	ledger.Observe(EventToolStarted, call, true)
	ledger.Observe(EventToolCompleted, call, true)
	if got := ledger.Snapshot()[0]; got.ExitCode != nil || got.Command != "" {
		t.Fatalf("replaceable tool name minted native facts: %#v", got)
	}
}

func TestExecutionEvidenceTracksFlagsAndFreshness(t *testing.T) {
	var ledger ExecutionLedger
	check := ToolPayload{ID: "test", Name: "shell", Turn: 1, Arguments: `{"command":"false | cat"}`}
	ledger.Observe(EventToolStarted, check, true)
	RecordExecution(ledger.Context(context.Background(), check.ID, check.Turn), turn.ExecutionEvidence{Command: "false | cat", Cwd: ".", ExitCode: new(1)})
	check.Output, check.Code = `{"command":"false | cat","cwd":".","exit_code":1}`, "exit 1"
	ledger.Observe(EventToolCompleted, check, true)
	read := ToolPayload{ID: "read", Name: "read_file", Turn: 2, Output: `{"exit_code":0}`}
	ledger.Observe(EventToolStarted, read, false)
	ledger.Observe(EventToolCompleted, read, false)
	got := ledger.Snapshot()
	if got[0].Outcome != "flagged" || got[0].ExitCode == nil || *got[0].ExitCode != 1 || got[0].Freshness != "current" || got[1].ExitCode != nil {
		t.Fatalf("unrelated read erased failure or forged execution status: %#v", got)
	}
	write := ToolPayload{ID: "edit", Name: "apply_patch", Turn: 3}
	ledger.Observe(EventToolStarted, write, true)
	ledger.Observe(EventToolCompleted, write, true)
	if got := ledger.Snapshot(); got[0].Freshness != "stale" || got[1].Freshness != "stale" {
		t.Fatalf("later write did not stale previous evidence: %#v", got)
	}
	if got[0].Freshness != "current" {
		t.Fatal("snapshot aliases mutable ledger")
	}
}

func TestExecutionEvidenceConcurrentCallsAreUncertain(t *testing.T) {
	var ledger ExecutionLedger
	a := ToolPayload{ID: "a", Name: "shell"}
	b := ToolPayload{ID: "b", Name: "apply_patch"}
	ledger.Observe(EventToolStarted, a, true)
	ledger.Observe(EventToolStarted, b, true)
	ledger.Observe(EventToolCompleted, b, true)
	ledger.Observe(EventToolCompleted, a, true)
	for _, item := range ledger.Snapshot() {
		if item.Freshness != "uncertain" {
			t.Fatalf("completion order mistaken for non-overlap: %#v", item)
		}
	}
	ledger.Reset()
	if got := ledger.Snapshot(); got == nil || len(got) != 0 {
		t.Fatalf("new request retained old evidence or lacked explicit empty ledger: %#v", got)
	}
}

func TestNativeFactsSurviveFilteringAndReadOnlyPreparation(t *testing.T) {
	var l ExecutionLedger
	check := ToolPayload{ID: "test", Name: "shell"}
	l.Observe(EventToolStarted, check, true)
	RecordExecution(l.Context(context.Background(), check.ID, 0), turn.ExecutionEvidence{Command: "go test ./...", ExitCode: new(1)})
	check.Output = `{"exit_code":0}`
	check.Code = "exit 1"
	l.Observe(EventToolCompleted, check, true)
	read := ToolPayload{ID: "read", Name: "shell"}
	l.Observe(EventToolStarted, read, true)
	RecordExecution(l.Context(context.Background(), read.ID, 0), turn.ExecutionEvidence{Command: "cat README.md", ExitCode: new(0), ReadOnly: true})
	l.Observe(EventToolCompleted, read, true)
	got := l.Snapshot()
	if *got[0].ExitCode != 1 || got[0].Freshness != "current" || got[0].Command != "go test ./..." {
		t.Fatalf("filtered/native readonly facts lost: %#v", got)
	}
}
func TestPersistentExecutionSurvivesResetAndPropagatesToParent(t *testing.T) {
	var parent, child ExecutionLedger
	call := ToolPayload{ID: "delegate", Name: "delegate_task"}
	parent.Observe(EventToolStarted, call, true)
	parentCtx := parent.Context(context.Background(), call.ID, 0)
	child.Observe(EventToolStarted, ToolPayload{ID: "launch"}, true)
	childCtx := child.Context(parentCtx, "launch", 0)
	RecordExecution(childCtx, turn.ExecutionEvidence{Persistent: true})
	for _, l := range []*ExecutionLedger{&parent, &child} {
		l.Reset()
		l.Observe(EventToolStarted, ToolPayload{ID: "next"}, false)
		l.Observe(EventToolCompleted, ToolPayload{ID: "next"}, false)
		if got := l.Snapshot()[0]; got.Freshness != "uncertain" {
			t.Fatalf("persistent writer forgotten: %#v", got)
		}
	}
	// A late result from the old request must not modify the new request's record.
	RecordExecution(childCtx, turn.ExecutionEvidence{Command: "late", ExitCode: new(0)})
	if child.Snapshot()[0].Command != "" {
		t.Fatal("late result contaminated new request")
	}
}
