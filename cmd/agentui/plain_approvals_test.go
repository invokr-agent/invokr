package agentui

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
)

// TestServePlainApprovalsHonorsStandingGrants covers piped mode: a standing
// grant approves, anything else is denied instead of blocking on a user who
// has no terminal to answer from.
func TestServePlainApprovalsHonorsStandingGrants(t *testing.T) {
	store := approval.NewStore("")
	if _, err := store.Add(approval.Grant{
		Tool:       "shell",
		ArgPattern: "git status*",
		Scope:      approval.ScopeGlobal,
		ExpiresAt:  time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	broker := approval.NewBroker()
	stop := denyPlainApprovals(broker, store, io.Discard)
	defer stop()

	decision, err := broker.RequestApproval(context.Background(),
		approval.Request{Tool: "shell", Subject: "git status --short"})
	if err != nil || !decision.Approved {
		t.Fatalf("granted call not approved: %+v err=%v", decision, err)
	}

	if _, err := broker.RequestApproval(context.Background(),
		approval.Request{Tool: "shell", Subject: "rm -rf /"}); err == nil {
		t.Fatal("ungranted call was approved")
	}
}

// TestServePlainApprovalsWithoutGrantStoreDenies keeps the nil-store path
// deny-by-default.
func TestServePlainApprovalsWithoutGrantStoreDenies(t *testing.T) {
	broker := approval.NewBroker()
	stop := denyPlainApprovals(broker, nil, io.Discard)
	defer stop()
	if _, err := broker.RequestApproval(context.Background(),
		approval.Request{Tool: "shell", Subject: "git status"}); err == nil {
		t.Fatal("nil grant store approved a call")
	}
}
