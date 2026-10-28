package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
)

func grantedStore(t *testing.T) *approval.Store {
	t.Helper()
	store := approval.NewStore("")
	if _, err := store.Add(approval.Grant{
		Tool:       "shell",
		ArgPattern: "git status*",
		Scope:      approval.ScopeGlobal,
		ExpiresAt:  time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

// TestServeExecApprovalsHonorsStandingGrants locks in that a standing grant
// made in the TUI still approves in `invokr exec`, where the approval queue
// that used to be the only grant reader does not exist.
func TestServeExecApprovalsHonorsStandingGrants(t *testing.T) {
	broker := approval.NewBroker()
	stop := serveExecApprovals(broker, grantedStore(t), approval.NewTierPolicy(false, nil))
	defer stop()

	decision, err := broker.RequestApproval(context.Background(),
		approval.Request{Tool: "shell", Subject: "git status --short"})
	if err != nil {
		t.Fatalf("granted call denied: %v", err)
	}
	if !decision.Approved || decision.Reason == "" {
		t.Fatalf("unexpected decision: %+v", decision)
	}

	if _, err := broker.RequestApproval(context.Background(),
		approval.Request{Tool: "shell", Subject: "rm -rf /"}); err == nil {
		t.Fatal("ungranted call was approved")
	}
}
