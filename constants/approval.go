package constants

const (
	ApprovalPolicyAuto = "auto"
	ApprovalPolicyAsk  = "ask"
	ApprovalPolicyDeny = "deny"
	// ApprovalPolicyAskOnce prompts in Auto until a standing grant covers the call.
	ApprovalPolicyAskOnce = "ask_once"
	// ApprovalPolicyAlwaysAsk prompts on every call in Auto; standing grants are ignored.
	ApprovalPolicyAlwaysAsk = "always_ask"
)
