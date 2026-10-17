package approval

import (
	"fmt"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/constants"
)

// TierPolicy is the unattended approval rule exec and scheduled jobs share:
// approve everything, or only requests whose every tier is allowed.
type TierPolicy struct {
	allowAll bool
	tiers    map[string]bool
}

// NewTierPolicy builds the matcher; "read-only" expands to the read-only shell
// and code-interpreter tiers.
func NewTierPolicy(allowAll bool, tiers []string) TierPolicy {
	policy := TierPolicy{allowAll: allowAll, tiers: make(map[string]bool)}
	for _, tier := range tiers {
		tier = strings.TrimSpace(tier)
		if tier == "read-only" {
			policy.tiers[constants.ShellTierReadonly] = true
			policy.tiers[constants.CodeInterpreterTierReadWorkspace] = true
		} else if tier != "" {
			policy.tiers[tier] = true
		}
	}
	return policy
}

// Approves reports whether req may run without a human.
func (p TierPolicy) Approves(req Request) bool {
	if p.allowAll {
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
