package toolbox

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
)

type countingApprover struct{ calls int }

func (a *countingApprover) RequestApproval(context.Context, approval.Request) (approval.Decision, error) {
	a.calls++
	return approval.Decision{Approved: true}, nil
}

// In Auto, plain "ask" lets a confined call through; ask_once and always_ask must prompt.
func TestAutoModeApprovalPolicies(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("confinement needs a sandbox")
	}
	for policy, wantPrompt := range map[string]bool{"ask": false, "ask_once": true, "always_ask": true} {
		ctx, done := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{})
		approver := &countingApprover{}
		tool := &executableTool{ToolSpec: core.ToolSpec{Name: "t", Metadata: core.ToolMetadata{Policy: core.ToolPolicy{Tiers: []string{"network"}, Approval: policy}}}}
		if err := approveToolboxCall(ctx, approver, tool, &core.ToolCall{Arguments: []byte(`{}`)}); err != nil || (approver.calls == 1) != wantPrompt {
			t.Errorf("%s: err=%v prompts=%d", policy, err, approver.calls)
		}
		done()
	}
}
