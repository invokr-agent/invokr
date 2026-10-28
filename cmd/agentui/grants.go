package agentui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
)

// grantUsageLine is the row /help prints for the command; grantsUsage is the
// same line carrying the "usage: " prefix a usage error reads with. The row
// cannot take that prefix: /help paints the text before the first space as the
// command name, so it would read "usage:" as the command and list /grants
// under a row that is not a command.
const (
	grantUsageLine = "/grants [list | add <tool> <hours> <pattern…> | deny <tool> <hours> <pattern…> | revoke <id>]"
	grantsUsage    = "usage: " + grantUsageLine
)

// maxGrantHours caps a standing grant at 30 days: a grant that never expires is
// indistinguishable from --allow-all.
const maxGrantHours = 720

func (m *model) grantsCommand(args []string) string {
	if m.grants == nil {
		return "standing grants are disabled"
	}
	if len(args) == 0 || args[0] == "list" {
		return renderGrants(m.grants.List())
	}
	switch args[0] {
	case "revoke":
		if len(args) != 2 {
			return "usage: /grants revoke <id>"
		}
		removed, err := m.grants.Revoke(args[1])
		if !removed {
			return "no standing rule with id " + args[1]
		}
		if err != nil {
			return "revoked rule " + args[1] + " for this session, but it could NOT be removed from disk and will return on restart: " + err.Error()
		}
		return "revoked rule " + args[1] + " (applies to the next call); ask-mode prompting resumes for that pattern"
	case "add", "deny":
		return m.addRule(args[1:], args[0] == "deny")
	}
	return grantsUsage
}

// ruleVerb names a rule's direction. A denial that reads as a grant in the
// listing is worse than no listing at all.
var ruleVerb = map[bool]string{false: "allow", true: "deny"}

// addRule creates a standing grant (deny=false) or standing denial (deny=true).
// Both take the same scope/expiry/pattern arguments; only the direction differs.
func (m *model) addRule(args []string, deny bool) string {
	verb := ruleVerb[deny]
	if len(args) < 3 {
		return grantsUsage
	}
	hours, err := strconv.ParseFloat(args[1], 64)
	if err != nil || !(hours > 0 && hours <= maxGrantHours) {
		return fmt.Sprintf("rule duration must be a number of hours in (0, %d]", maxGrantHours)
	}
	rule, err := m.grants.Add(approval.Grant{
		Deny:       deny,
		Tool:       args[0],
		ArgPattern: unquotePattern(strings.Join(args[2:], " ")),
		Scope:      approval.ScopeGlobal,
		ExpiresAt:  time.Now().Add(time.Duration(hours * float64(time.Hour))),
		Reason:     verb + "ed from /grants",
	})
	if err != nil {
		return verb + " refused: " + err.Error()
	}
	return fmt.Sprintf("%sed %s %q until %s (id %s)", verb, rule.Tool, rule.ArgPattern, rule.ExpiresAt.Format(time.RFC3339), rule.ID)
}

// unquotePattern strips one layer of matching surrounding quotes. Users copy
// the documented `/grants add shell 2 'git status*'` form verbatim, and a
// pattern stored with its quotes attached can never match anything.
func unquotePattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if len(pattern) >= 2 {
		if q := pattern[0]; (q == '\'' || q == '"') && pattern[len(pattern)-1] == q {
			return strings.TrimSpace(pattern[1 : len(pattern)-1])
		}
	}
	return pattern
}

func renderGrants(grants []approval.Grant) string {
	if len(grants) == 0 {
		return "no standing rules"
	}
	var b strings.Builder
	b.WriteString("─ standing rules (deny wins over grant) ────\n")
	for _, g := range grants {
		fmt.Fprintf(&b, "%s  %-5s  %s  %q  %s  expires %s\n", g.ID, ruleVerb[g.Deny], g.Tool, g.ArgPattern, g.Scope, g.ExpiresAt.Format(time.RFC3339))
	}
	return strings.TrimRight(b.String(), "\n")
}
