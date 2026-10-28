package policy

import (
	"testing"

	"github.com/invokr-agent/invokr/constants"
)

func TestDefaultRecallPolicyUsesConstants(t *testing.T) {
	p := Default

	if p.PromptBudgetChars != constants.MemoryRecallPromptBudgetChars {
		t.Fatalf("PromptBudgetChars = %d, want %d", p.PromptBudgetChars, constants.MemoryRecallPromptBudgetChars)
	}
	if !p.Tier1.Enabled || !p.Tier1.PromptEnabled || !p.Tier1.RecallToolEnabled || p.Tier1.Backend != constants.MemoryRecallTier1Backend {
		t.Fatalf("unexpected tier1 policy: %#v", p.Tier1)
	}
	if !p.Tier2.Enabled || p.Tier2.PromptEnabled || !p.Tier2.RecallToolEnabled || p.Tier2.Backend != constants.MemoryRecallTier2Backend || p.Tier2.Limit != constants.MemoryRecallTier2Limit {
		t.Fatalf("unexpected tier2 policy: %#v", p.Tier2)
	}
}
