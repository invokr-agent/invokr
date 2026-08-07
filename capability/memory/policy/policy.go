package policy

import "github.com/invokr-ai/invokr/constants"

type Tier struct {
	Enabled, PromptEnabled, RecallToolEnabled                         bool
	Backend                                                           string
	Limit                                                             int
	IncludeMarkdownUser, IncludeMarkdownProject, IncludeMarkdownAgent bool
}

type Policy struct {
	PromptBudgetChars int
	Tier1             Tier
	Tier2             Tier
}

var Default = Policy{PromptBudgetChars: constants.MemoryRecallPromptBudgetChars,
	Tier1: Tier{Enabled: true, PromptEnabled: true, RecallToolEnabled: true, Backend: constants.MemoryRecallTier1Backend,
		IncludeMarkdownUser: true, IncludeMarkdownProject: true, IncludeMarkdownAgent: true},
	Tier2: Tier{Enabled: true, PromptEnabled: false, RecallToolEnabled: true,
		Backend: constants.MemoryRecallTier2Backend, Limit: constants.MemoryRecallTier2Limit}}
