package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

func TestNewAgentSessionKeepsMemoryAndPersistentGrants(t *testing.T) {
	provider := providertest.NewScripted(t)
	defer provider.Close()
	app := runtimeTestApp(provider.URL(), t.TempDir())
	app.Approvals.GrantsFile = filepath.Join(app.Memory.Dir, "grants.json")
	rt, err := Bootstrap(app, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Cleanup() }()
	old := rt.Services()
	defer old.Session.(interface{ Close() }).Close()
	if _, err := old.Memory.Save(context.Background(), core.Record{Kind: core.KindFact, Source: core.SourceMarkdown, Content: "keep this memory"}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{approval.ScopeSession, approval.ScopeGlobal} {
		if _, err := old.Grants.Add(approval.Grant{Tool: "shell", ArgPattern: "git status", Scope: scope, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	next, err := old.NewAgentSession()
	if err != nil {
		t.Fatal(err)
	}
	defer next.Session.(interface{ Close() }).Close()
	if old.Session.SessionID() == next.Session.SessionID() || old.Approvals == next.Approvals || old.Commands == next.Commands {
		t.Fatal("session-scoped runtime objects were reused")
	}
	if old.PromptHistory != next.PromptHistory {
		t.Fatal("new session replaced prompt history")
	}
	if records, err := next.Memory.List(context.Background(), core.Query{}); err != nil || len(records) != 1 || records[0].Content != "keep this memory" {
		t.Fatalf("persistent memory lost: %+v (%v)", records, err)
	}
	if grants := next.Grants.List(); len(grants) != 1 || grants[0].Scope != approval.ScopeGlobal {
		t.Fatalf("temporary grants survived or persistent grants were lost: %+v", grants)
	}
}
