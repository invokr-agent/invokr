package approval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A denial must fire when it matches ANY path in a path-list subject: the model
// must not be able to escape it — into a broader grant, no less — by adding a
// second, unrelated file to the same patch.
func TestDenialMatchesAnyPathInPathListSubject(t *testing.T) {
	store := newTestStore(t)
	grant := testGrant(t, "docs/*")
	grant.Tool = "apply_patch"
	mustAdd(t, store, grant)
	deny := denial(t, "docs/secret*")
	deny.Tool = "apply_patch"
	stored := mustAdd(t, store, deny)
	req := Request{
		Tool:              "apply_patch",
		Subject:           "docs/secret.md docs/readme.md",
		SubjectIsPathList: true,
	}
	decision, ok := store.Decide(req)
	if !ok || decision.Approved {
		t.Fatalf("Decide = (%+v, %v), want a denial", decision, ok)
	}
	if decision.Reason != "standing denial "+stored.ID {
		t.Fatalf("reason = %q, want the denial id", decision.Reason)
	}
	// A path list with no denied path is still covered by the standing grant.
	clean := Request{Tool: "apply_patch", Subject: "docs/a.md docs/b.md", SubjectIsPathList: true}
	if decision, ok := store.Decide(clean); !ok || !decision.Approved {
		t.Fatalf("Decide(clean) = (%+v, %v), want the standing grant", decision, ok)
	}
}

// The pre-denial on-disk shape was a bare JSON array, and `deny` is omitempty
// inside a record that binary otherwise understands — so if the array form were
// still accepted, a downgrade would silently read a stored denial as a grant and
// auto-approve it. Load must refuse the legacy shape instead: no rules, an
// error, and therefore prompting rather than allowing.
func TestLoadRefusesLegacyArrayFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	legacy := `[{"id":"a","tool":"shell","arg_pattern":"git push*","scope":"global","expires_at":"2999-01-01T00:00:00Z"}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write grants file: %v", err)
	}
	store := NewStore(path)
	if err := store.Load(); err == nil {
		t.Fatal("Load accepted the legacy array form, want a parse error")
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("loaded %d rules from a refused file, want 0", len(got))
	}
}

// A denial must survive a save/load round trip through the versioned file, or
// "always deny" is only as durable as the process.
func TestDenialSurvivesFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	stored := mustAdd(t, NewStore(path), denial(t, "git push*"))
	reloaded := NewStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	decision, ok := reloaded.Decide(Request{Tool: "shell", Subject: "git push --force"})
	if !ok || decision.Approved {
		t.Fatalf("Decide after reload = (%+v, %v), want a denial", decision, ok)
	}
	if decision.Reason != "standing denial "+stored.ID {
		t.Fatalf("reason = %q, want the denial id", decision.Reason)
	}
}

// denial returns a standing denial for pattern, the mirror of testGrant.
func denial(t *testing.T, pattern string) Grant {
	t.Helper()
	g := testGrant(t, pattern)
	g.Deny = true
	return g
}

// TestDecidePrecedenceDenyOverridesGrant is the precedence-resolver test: with
// both an applicable grant and an applicable denial, the call is denied.
func TestDecidePrecedenceDenyOverridesGrant(t *testing.T) {
	store := newTestStore(t)
	mustAdd(t, store, testGrant(t, "git push*"))
	stored := mustAdd(t, store, denial(t, "git push*"))
	req := Request{Tool: "shell", Subject: "git push --force"}
	decision, ok := store.Decide(req)
	if !ok || decision.Approved {
		t.Fatalf("Decide = (%+v, %v), want a denial", decision, ok)
	}
	if decision.Reason != "standing denial "+stored.ID {
		t.Fatalf("reason = %q, want the denial id", decision.Reason)
	}
	// Order must not matter: a grant added after the denial still loses.
	mustAdd(t, store, testGrant(t, "git push --force*"))
	if decision, _ := store.Decide(req); decision.Approved {
		t.Fatalf("a later grant overrode a standing denial: %+v", decision)
	}
}

func TestDecideReturnsGrantWhenNoDenialApplies(t *testing.T) {
	store := newTestStore(t)
	stored := mustAdd(t, store, testGrant(t, "git status*"))
	mustAdd(t, store, denial(t, "git push*"))
	decision, ok := store.Decide(Request{Tool: "shell", Subject: "git status --short"})
	if !ok || !decision.Approved || decision.Reason != "standing grant "+stored.ID {
		t.Fatalf("Decide = (%+v, %v), want approval by %s", decision, ok, stored.ID)
	}
	if _, ok := store.Decide(Request{Tool: "shell", Subject: "git log"}); ok {
		t.Fatalf("an unmatched call produced a standing decision")
	}
}

// TestDenialCoversExcludedTool: the exclusion list stops a tool being
// auto-approved. Auto-denying it is always safe, so denials ignore the list.
func TestDenialCoversExcludedTool(t *testing.T) {
	store := newTestStore(t)
	g := denial(t, "make me a tool*")
	g.Tool = "create_tool"
	mustAdd(t, store, g)
	decision, ok := store.Decide(Request{Tool: "create_tool", Subject: "make me a tool now"})
	if !ok || decision.Approved {
		t.Fatalf("Decide = (%+v, %v), want a denial for an excluded tool", decision, ok)
	}
}

func TestDenialScopeAndExpiry(t *testing.T) {
	store := newTestStore(t)
	ws := denial(t, "git push*")
	ws.Scope = ScopeWorkspace
	ws.Workspace = "/repo/a"
	mustAdd(t, store, ws)
	expired := denial(t, "git tag*")
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	expired.ID = "expired"
	store.grants = append(store.grants, expired)

	req := Request{Tool: "shell", Subject: "git push origin"}
	store.SetWorkspace("/repo/b")
	if _, ok := store.Decide(req); ok {
		t.Fatalf("workspace denial applied outside its workspace")
	}
	store.SetWorkspace("/repo/a")
	if decision, ok := store.Decide(req); !ok || decision.Approved {
		t.Fatalf("workspace denial did not apply in its workspace: %+v %v", decision, ok)
	}
	if _, ok := store.Decide(Request{Tool: "shell", Subject: "git tag v1"}); ok {
		t.Fatalf("expired denial still applied")
	}
}

// TestDenialSurvivesReload covers the resume path: a persisted denial is still
// in force after the store is rebuilt from disk, and revoking it restores
// ask-mode prompting.
func TestDenialSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	stored := mustAdd(t, NewStore(path), denial(t, "git push*"))

	second := NewStore(path)
	if err := second.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	req := Request{Tool: "shell", Subject: "git push origin"}
	decision, ok := second.Decide(req)
	if !ok || decision.Approved || decision.Reason != "standing denial "+stored.ID {
		t.Fatalf("reloaded store = (%+v, %v), want the persisted denial", decision, ok)
	}
	if removed, err := second.Revoke(stored.ID); !removed || err != nil {
		t.Fatalf("Revoke = (%v, %v)", removed, err)
	}
	if _, ok := second.Decide(req); ok {
		t.Fatalf("revoked denial still decided; ask-mode prompting would not resume")
	}
}

func TestAddAuditsRuleCreationAndUse(t *testing.T) {
	store := newTestStore(t)
	var seen []string
	store.SetAuditor(func(g Grant, _ Request, event string) {
		seen = append(seen, event+":"+ruleWord(g.Deny))
	})
	mustAdd(t, store, denial(t, "git push*"))
	if _, ok := store.Decide(Request{Tool: "shell", Subject: "git push origin"}); !ok {
		t.Fatalf("denial did not match")
	}
	if len(seen) != 2 || seen[0] != "created:deny" || seen[1] != "used:deny" {
		t.Fatalf("audit events = %v, want created then used", seen)
	}
}

// TestQueueShortCircuitsStandingDenial: a denied call must never reach the
// visible queue, and the requester must see a typed DeniedError (classified
// with errors.As, never by matching the message text).
func TestQueueShortCircuitsStandingDenial(t *testing.T) {
	store := newTestStore(t)
	mustAdd(t, store, testGrant(t, "git push*")) // deny still wins
	mustAdd(t, store, denial(t, "git push*"))
	queue := &ApprovalQueue{}
	queue.SetStandingGrants(store)

	broker := NewBroker()
	go func() { queue.enqueue(<-broker.Requests()) }()
	_, err := broker.RequestApproval(context.Background(), Request{Tool: "shell", Subject: "git push origin"})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("RequestApproval err = %v, want a *DeniedError", err)
	}
	if queue.Len() != 0 {
		t.Fatalf("a standing denial left %d items prompting", queue.Len())
	}
}

func ruleWord(deny bool) string {
	if deny {
		return "deny"
	}
	return "grant"
}
