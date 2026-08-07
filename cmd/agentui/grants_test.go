package agentui

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
)

func TestAddGrantRejectsNonFiniteHours(t *testing.T) {
	m := &model{grants: approval.NewStore("")}
	for _, hours := range []string{"NaN", "+Inf", "0", "-1", "721"} {
		out := m.addRule([]string{"shell", hours, "git status*"}, false)
		if !strings.Contains(out, "rule duration must be") {
			t.Fatalf("hours=%s: expected rejection, got %q", hours, out)
		}
	}
	if got := m.grants.List(); len(got) != 0 {
		t.Fatalf("expected no grants stored, got %d", len(got))
	}
}

func TestAddGrantStripsSurroundingQuotes(t *testing.T) {
	m := &model{grants: approval.NewStore("")}
	for _, arg := range []string{"'git status*'", `"git status*"`} {
		out := m.addRule([]string{"shell", "2", arg}, false)
		if !strings.Contains(out, `allowed shell "git status*"`) {
			t.Fatalf("arg=%s: got %q", arg, out)
		}
	}
	for _, g := range m.grants.List() {
		if g.ArgPattern != "git status*" {
			t.Fatalf("stored pattern = %q", g.ArgPattern)
		}
	}
}

func TestAddGrantAllowsShellAndRefusesCodeInterpreter(t *testing.T) {
	m := &model{grants: approval.NewStore("")}
	if out := m.addRule([]string{"shell", "2", "git status*"}, false); !strings.HasPrefix(out, "allowed shell") {
		t.Fatalf("shell must be grantable, got %q", out)
	}
	for _, tool := range []string{"code_interpreter", "create_tool", "delegate_task"} {
		if out := m.addRule([]string{tool, "2", "print-hello*"}, false); !strings.Contains(out, "cannot be covered by a standing grant") {
			t.Fatalf("%s: expected refusal, got %q", tool, out)
		}
	}
}
