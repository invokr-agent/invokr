package approval

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/constants"
)

// TierPolicy is the unattended approval rule exec and scheduled jobs share:
// approve everything, or only requests whose every tier is allowed.
type TierPolicy struct {
	allowAll, tests bool
	tiers           map[string]bool
}

// tierAliases are the user-facing names --allow takes for the shell tiers.
var tierAliases = map[string]string{
	"write":   constants.ShellTierWrite,
	"network": constants.ShellTierNetwork,
	// Parsed by its own case (it also opens the read-workspace interpreter
	// tier); listed so a hint names it.
	"read-only": constants.ShellTierReadonly,
}

// NewTierPolicy builds the matcher; "read-only" expands to the read-only shell
// and code-interpreter tiers, "write"/"network" name the shell tiers, and
// "tests" approves a project's test command alone (see testCommand).
func NewTierPolicy(allowAll bool, tiers []string) TierPolicy {
	policy := TierPolicy{allowAll: allowAll, tiers: make(map[string]bool)}
	for _, tier := range tiers {
		switch tier = strings.TrimSpace(tier); tier {
		case "":
		case "read-only":
			policy.tiers[constants.ShellTierReadonly] = true
			policy.tiers[constants.CodeInterpreterTierReadWorkspace] = true
		case "tests":
			policy.tests = true
		default:
			policy.tiers[cmp.Or(tierAliases[tier], tier)] = true
		}
	}
	return policy
}

// testRunners are the commands --allow tests lets run: a project's own test
// entry points, which build and run its code but are what a "why does this
// fail" question needs.
var testRunners = []string{"go test", "go vet", "npm test", "npm run test", "pnpm test", "yarn test", "cargo test", "pytest", "python -m pytest", "make test"}

// testCommand reports a shell subject that is only a test run: a known runner,
// optionally piped through head/tail/grep, with no other command chained in.
func testCommand(subject string) bool {
	subject = strings.ReplaceAll(subject, "2>&1", "")
	if strings.ContainsAny(subject, ";&`$<>()\n") {
		return false
	}
	stages := strings.Split(subject, "|")
	head := strings.Join(strings.Fields(stages[0]), " ")
	// go test -exec/-toolexec run an arbitrary program in the test's place.
	if slices.ContainsFunc(strings.Fields(head), func(f string) bool {
		name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "=")
		return strings.HasPrefix(f, "-") && (name == "exec" || name == "toolexec")
	}) {
		return false
	}
	if !slices.ContainsFunc(testRunners, func(r string) bool { return head == r || strings.HasPrefix(head, r+" ") }) {
		return false
	}
	for _, stage := range stages[1:] {
		if f := strings.Fields(stage); len(f) == 0 || !slices.Contains([]string{"head", "tail", "grep"}, f[0]) {
			return false
		}
	}
	return true
}

// Approves reports whether req may run without a human.
func (p TierPolicy) Approves(req Request) bool {
	// Enabling a tool runs nothing: the enabled tool's own calls meet this
	// policy, so a run with no one to ask can still widen its toolset.
	if p.allowAll || req.Tier == "tool_enable" || p.tests && req.Tool == "shell" && testCommand(req.Subject) {
		return true
	}
	if strings.TrimSpace(req.Tier) == "" {
		return false
	}
	return !slices.ContainsFunc(strings.Split(req.Tier, ","), func(tier string) bool { return !p.tiers[strings.TrimSpace(tier)] })
}

// Stored job policies: PolicyAllowAll, PolicyDeny, or a comma list of tiers.
const (
	PolicyAllowAll = "allow"
	PolicyDeny     = "deny"
)

// EncodePolicy is the one writer of the stored form. A tier spelled like a
// reserved word is rejected: "--allow allow" must not mean allow-all.
func EncodePolicy(allowAll bool, tiers []string) (string, error) {
	if allowAll {
		return PolicyAllowAll, nil
	}
	reserved := func(t string) bool { t = strings.TrimSpace(t); return t == PolicyAllowAll || t == PolicyDeny }
	if slices.ContainsFunc(strings.Split(strings.Join(tiers, ","), ","), reserved) {
		return "", fmt.Errorf("--allow takes tier names; %q and %q are reserved (use --allow-all)", PolicyAllowAll, PolicyDeny)
	}
	if len(tiers) == 0 {
		return PolicyDeny, nil
	}
	return strings.Join(tiers, ","), nil
}

// ParsePolicy is the one reader of the stored form.
func ParsePolicy(stored string) TierPolicy {
	if stored == PolicyDeny {
		return NewTierPolicy(false, nil)
	}
	return NewTierPolicy(stored == PolicyAllowAll, strings.Split(stored, ","))
}
