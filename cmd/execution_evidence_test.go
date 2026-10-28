package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/platform/turn"
)

func TestExecFlagsAreSeparateAndStrictExitIsOptional(t *testing.T) {
	exit := 1
	resp := turn.AgentResponse{Message: "Tests passed", ExecutionEvidence: []turn.ExecutionEvidence{{CallID: "check", Tool: "shell", Command: "false | cat", Cwd: ".", Outcome: "flagged", Code: "exit 1", ExitCode: &exit, Freshness: "current"}}}
	for _, strict := range []bool{false, true} {
		var out bytes.Buffer
		err := completeExecResponse(&out, io.Discard, "s1", "model", resp, execOptions{jsonOutput: true, failOnToolError: strict})
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
	var out, errOut bytes.Buffer
	if err := completeExecResponse(&out, &errOut, "s1", "model", resp, execOptions{}); err != nil {
		t.Fatal(err)
	}
	// stdout is the answer alone; the guard goes to stderr (A2).
	if strings.TrimSpace(out.String()) != "Tests passed" {
		t.Fatalf("stdout carries more than the answer: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "Execution history — not final task status") || !strings.Contains(errOut.String(), "does not establish full task verification") || !strings.Contains(errOut.String(), "exit=1") {
		t.Fatalf("missing guard on stderr: %s", errOut.String())
	}
}
