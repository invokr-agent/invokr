package turn

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ExecutionEvidence records observations, not proof that a task or test suite passed.
// ExitCode is absent unless a native execution result supplied it. Cwd is workspace-relative.
type ExecutionEvidence struct {
	Persistent bool   `json:"persistent,omitempty"`
	ReadOnly   bool   `json:"read_only,omitempty"`
	CallID     string `json:"call_id"`
	Turn       int    `json:"turn"`
	Tool       string `json:"tool"`
	Command    string `json:"command,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Outcome    string `json:"outcome"`
	Code       string `json:"code,omitempty"`
	ExitCode   *int   `json:"exit_code"`
	Freshness  string `json:"freshness"`
	Started    int    `json:"started"`
	Finished   int    `json:"finished"`
}

func FlaggedExecution(evidence []ExecutionEvidence) []ExecutionEvidence {
	return slices.DeleteFunc(slices.Clone(evidence), func(item ExecutionEvidence) bool { return item.Outcome != "flagged" })
}

// Historical evidence is bounded in prompts; the full ledger remains on disk.
func AppendHistoricalExecution(message string, evidence []ExecutionEvidence) string {
	if len(evidence) == 0 {
		return message
	}
	counts := map[string]int{}
	last := ""
	for _, item := range evidence {
		counts[item.Outcome]++
		last = cmp.Or(item.Command, last)
	}
	outcomes := make([]string, 0, len(counts))
	for _, outcome := range slices.Sorted(maps.Keys(counts)) {
		outcomes = append(outcomes, fmt.Sprintf("%d %s", counts[outcome], outcome))
	}
	return fmt.Sprintf("%s\n\n[Earlier tool calls (historical, not a current check): %s; last command %.256q]", message, strings.Join(outcomes, ", "), last)
}

// AppendExecutionGuard is shared by plain, TUI and CLI rendering; nil is legacy/unavailable.
func AppendExecutionGuard(message string, evidence []ExecutionEvidence) string {
	if len(evidence) == 0 {
		return message
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n\n[Execution history — not final task status; does not establish full task verification]\n", strings.TrimSpace(message))
	for _, item := range evidence {
		fmt.Fprintf(&out, "- %s %q: %s (%s)", item.Tool, item.CallID, item.Outcome, item.Freshness)
		for _, field := range [][2]string{{"command", item.Command}, {"cwd", item.Cwd}, {"code", item.Code}} {
			if field[1] != "" {
				fmt.Fprintf(&out, " %s=%q", field[0], field[1])
			}
		}
		if item.ExitCode != nil {
			fmt.Fprintf(&out, " exit=%d", *item.ExitCode)
		}
		out.WriteByte('\n')
	}
	return strings.TrimSpace(out.String())
}
