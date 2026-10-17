package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/platform/turn"
)

func TestExecFlagsAreSeparateAndStrictExitIsOptional(t *testing.T) {
	exit := 1
	resp := turn.AgentResponse{Message: "Tests passed", ExecutionEvidence: []turn.ExecutionEvidence{{CallID: "check", Tool: "shell", Command: "false | cat", Cwd: ".", Outcome: "flagged", Code: "exit 1", ExitCode: &exit, Freshness: "current"}}}
	for _, strict := range []bool{false, true} {
		var out bytes.Buffer
		err := completeExecResponse(&out, "s1", "model", resp, execOptions{jsonOutput: true, failOnToolError: strict})
		if (err != nil) != strict {
			t.Fatalf("strict=%v err=%v", strict, err)
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(out.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if string(record["message"]) != `"Tests passed"` || string(record["tool_failures"]) != "null" || !bytes.Contains(record["tool_flagged"], []byte("exit 1")) {
			t.Fatalf("changed text/hard failures or missing flagged evidence: %s", out.String())
		}
	}
	var out bytes.Buffer
	if err := completeExecResponse(&out, "s1", "model", resp, execOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Execution history — not final task status") || !strings.Contains(out.String(), "does not establish full task verification") || !strings.Contains(out.String(), "exit=1") {
		t.Fatalf("missing guard: %s", out.String())
	}
}
