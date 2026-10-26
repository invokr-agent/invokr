package agentui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestHelpColumnsStayReadable: one long usage (/grants, /reasoning) must not
// push every description into a sliver; descriptions keep most of the width
// and a wrapped label never runs into its description.
func TestHelpColumnsStayReadable(t *testing.T) {
	out := ansi.Strip(renderCommandHelp(114))
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "/clear") && !strings.Contains(line, "start a new session (keep saved history and memory)") {
			t.Fatalf("description wrapped too narrow: %q", line)
		}
		if strings.Contains(line, "…>manage") || strings.Contains(line, "]manage") {
			t.Fatalf("long label runs into its description: %q", line)
		}
	}
}
