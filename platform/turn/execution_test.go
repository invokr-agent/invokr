package turn

import "testing"

func TestEmptyExecutionEvidenceLeavesMessageUnchanged(t *testing.T) {
	for _, evidence := range [][]ExecutionEvidence{nil, {}} {
		for _, format := range []func(string, []ExecutionEvidence) string{AppendHistoricalExecution, AppendExecutionGuard} {
			if got := format("Hi.", evidence); got != "Hi." {
				t.Fatalf("empty evidence changed greeting: %q", got)
			}
		}
	}
}
