//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/support/testutil/clitest"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

// seedStandingDenial writes one persisted standing denial to the home's grants
// file through the real store — the same writer the TUI's "always deny" action
// uses, so the fixture cannot drift from the shape the product persists.
func seedStandingDenial(t *testing.T, home testHome, pattern string) {
	t.Helper()
	store := approval.NewStore(home.app.Approvals.GrantsFile)
	if _, err := store.Add(approval.Grant{
		ID:         "d360-denial",
		Deny:       true,
		Tool:       "shell",
		ArgPattern: pattern,
		Scope:      approval.ScopeGlobal,
		CreatedAt:  time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
		Reason:     "always-denied from the approval queue",
	}); err != nil {
		t.Fatalf("seed standing denial: %v", err)
	}
}

// TestD360_StandingDenialShortCircuitsApproval covers issue #360: a persisted
// standing denial denies a matching call without prompting, wins over an
// explicit tier allowance (deny-overrides), still applies in a second process
// (the resume half), and stops applying once revoked.
func TestD360_StandingDenialShortCircuitsApproval(t *testing.T) {
	run := func(t *testing.T, home testHome) ledger {
		t.Helper()
		provider := providertest.NewScripted(t,
			shellCall("d360-write", "git", "branch", "d360-probe"),
			providertest.ReplyStep("d360-finished"),
		)
		defer provider.Close()
		result := runExec(t, home, provider.URL(), "create a branch", "--allow", "execute_write_command")
		if result.Err != nil {
			t.Fatalf("exec failed: %v\nstderr: %s", result.Err, result.Stderr)
		}
		provider.AssertClean(t)
		provider.AssertExhausted(t)
		return parseLedger(t, result.Stdout)
	}

	home := newHome(t, "")
	seedStandingDenial(t, home, "git branch d360*")

	got := run(t, home)
	if failures := got.failures(); len(failures) != 1 || failures[0].Tool != "shell" || failures[0].Kind != "denied" {
		t.Fatalf("standing denial did not deny the call: failures=%#v completed=%#v", failures, got.Completed)
	}
	if got.Completed["tool_call_count"] != float64(1) {
		t.Fatalf("tool_call_count = %#v, want 1", got.Completed["tool_call_count"])
	}

	// A second process reloads the same grants file: the denial survives a
	// restart/resume rather than living only in the first run's memory.
	if failures := run(t, home).failures(); len(failures) != 1 || failures[0].Kind != "denied" {
		t.Fatalf("denial did not survive a restart: failures=%#v", failures)
	}

	// Revoking restores normal policy: with the tier allowed, the same call now
	// runs instead of being denied.
	mustWrite(t, home.app.Approvals.GrantsFile, "[]")
	if failures := run(t, home).failures(); len(failures) != 0 {
		t.Fatalf("call still denied after revoking the standing denial: %#v", failures)
	}
}

// TestD360_PlainModeHonorsStandingDenial checks the non-TUI path reports the
// standing denial rather than the generic "no terminal" auto-deny.
func TestD360_PlainModeHonorsStandingDenial(t *testing.T) {
	provider := providertest.NewScripted(t,
		shellCall("d360-plain", "git", "branch", "d360-probe"),
		providertest.ReplyStep("d360-plain-finished"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	seedStandingDenial(t, home, "git branch d360*")

	result := clitest.Run(t, clitest.Options{
		State:       home.state,
		CommandArgs: []string{"--base-url=" + provider.URL(), "agent"},
		Input:       "create a branch\nexit\n",
		Timeout:     30 * time.Second,
		Env:         execEnv(nil),
	})
	if result.TimedOut || result.Err != nil {
		t.Fatalf("plain run failed: timedOut=%v err=%v\nstderr: %s", result.TimedOut, result.Err, result.Stderr)
	}
	if !strings.Contains(result.Stderr, "auto-denied: standing denial d360-denial") {
		t.Fatalf("plain mode did not report the standing denial:\n%s", result.Stderr)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
