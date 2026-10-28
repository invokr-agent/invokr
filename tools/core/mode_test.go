package core

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
)

func TestPreparedFactsOverrideAdvertisedReadOnly(t *testing.T) {
	p := approval.NewPolicy(approval.Auto, false)
	ctx, done := p.Begin(context.Background(), approval.Request{Tool: "plugin", ReadOnly: true})
	defer done()
	err := EnforceApproval(ctx, nil, ToolSpec{Name: "plugin"}, Automatic(approval.Request{Tool: "plugin", Writes: true}))
	if err == nil {
		t.Fatal("unconfined code approved from advertised read-only capabilities")
	}
}

func TestAutomaticRequestChecksDenials(t *testing.T) {
	p := approval.NewPolicy(approval.Plan, false)
	ctx, done := p.Begin(context.Background(), approval.Request{Tool: "custom"})
	defer done()
	tool := Tool[struct{}]{Spec: ToolSpec{Name: "custom"}, Execute: func(context.Context, struct{}) (string, error) {
		t.Fatal("unguarded automatic tool executed")
		return "", nil
	}}
	if _, err := tool.Handle(ctx, &ToolCall{Name: "custom"}); err == nil {
		t.Fatal("nil Approve bypassed Plan")
	}
}
