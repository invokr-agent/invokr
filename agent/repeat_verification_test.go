package agent

import (
	"fmt"
	"testing"
)

func buildCall(result string) toolCallSignature {
	return toolCallSignature{Name: "shell", Key: "shell-build", Args: "go build ./...", Result: result}
}

// An identical call returning an identical result three times in a row trips
// the guard.
func TestRepeatGuardStillFiresOnIdenticalResults(t *testing.T) {
	var guard repeatGuard
	for i := range maxIdenticalToolCallRepeats - 1 {
		if guard.observe(turnSignatureKey([]toolCallSignature{buildCall("same")}), "", "") {
			t.Fatalf("guard fired early at %d", i)
		}
	}
	if !guard.observe(turnSignatureKey([]toolCallSignature{buildCall("same")}), "", "") {
		t.Fatal("identical call with an identical result was not detected")
	}
}

// The same call returning different output each time is progress.
func TestRepeatGuardIgnoresDifferingResults(t *testing.T) {
	var guard repeatGuard
	for i := range maxIdenticalToolCallRepeats + 2 {
		if guard.observe(turnSignatureKey([]toolCallSignature{buildCall(fmt.Sprintf("out-%d", i))}), "", "") {
			t.Fatalf("guard fired on differing results at %d", i)
		}
	}
}

// A rebuild after a successful edit never trips the guard: the build's own
// result changes across cycles (differing output is progress), and each
// edit call carries a distinct key.
func TestRepeatGuardSparesBuildAfterEditCycles(t *testing.T) {
	var guard repeatGuard
	for cycle := range 5 {
		edit := toolCallSignature{Name: "edit", Key: fmt.Sprintf("edit-%d", cycle), Result: "ok"}
		if guard.observe(turnSignatureKey([]toolCallSignature{edit}), "", "") {
			t.Fatalf("cycle %d: edit call flagged as a repeat", cycle)
		}
		build := buildCall(fmt.Sprintf("BUILD DONE %d", cycle))
		if guard.observe(turnSignatureKey([]toolCallSignature{build}), "", "") {
			t.Fatalf("cycle %d: build call flagged as a repeat", cycle)
		}
	}
}
