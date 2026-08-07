package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
)

func TestShellPipelineStatusCannotHideFailure(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
	}{
		{"false | cat", 1},
		{"printf ok | cat", 0},
		{"yes | head -n 1", 0},
		{"seq 1 200000 | head -n 1", 0},
		{"false | yes | head -n 1", 1},
		{"exit 141", 141},
		{"set +o pipefail; false | cat", 1},
		{"seq 1 200000 | head -1 >/dev/null && echo NEXT-RAN", 0},
		{"sh -c 'exit 3' | head", 3},
	} {
		t.Run(tc.line, func(t *testing.T) {
			dir := t.TempDir()
			plan := preparedShellCommand{line: tc.line, command: "bash", cwd: dir, workspaceRoot: dir, roots: []string{dir}, commands: []string{"bash", "cat", "yes", "head", "seq", "sh"}, timeoutMS: 5000, maxOutputBytes: 1000}
			for _, mode := range []approval.Mode{approval.Manual, approval.YOLO} {
				plan.mode = mode
				var ledger events.ExecutionLedger
				payload := events.ToolPayload{ID: "call", Name: "shell"}
				ledger.Observe(events.EventToolStarted, payload, true)
				result, err := runLine(nil, ledger.Context(context.Background(), "call", 0), plan)
				if err != nil || result.ExitCode != tc.want {
					t.Fatalf("mode %s exit=%d err=%v, want %d", mode, result.ExitCode, err, tc.want)
				}
				if strings.Contains(tc.line, "NEXT-RAN") && !strings.Contains(result.Stdout, "NEXT-RAN") {
					t.Fatalf("pipeline skipped tail: %+v", result)
				}
				ledger.Observe(events.EventToolCompleted, events.ToolPayload{ID: "call", Name: "shell", Output: `{"exit_code":999}`}, true)
				facts := ledger.Snapshot()[0]
				if facts.ExitCode == nil || *facts.ExitCode != tc.want || facts.Command != tc.line {
					t.Fatalf("native evidence lost: %+v", facts)
				}

			}
		})
	}
}
