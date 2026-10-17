//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestT09_PlainModeAutoDeniesApproval covers plan case T09: with stdin piped
// there is no terminal to answer an approval on, so the request must be
// auto-denied and the turn must finish. Before the fix the pipeline hung
// forever with no prompt, no timeout and no ledger entry — hence the explicit
// run timeout here.
func TestT09_PlainModeAutoDeniesApproval(t *testing.T) {
	provider := providertest.NewScripted(t,
		shellCall("t09-write", "git", "branch", "t09-probe"),
		providertest.ReplyStep("t09-finished"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())

	result := clitest.Run(t, clitest.Options{
		State:       home.state,
		CommandArgs: []string{"--base-url=" + provider.URL(), "agent"},
		Input:       "create a branch\nexit\n",
		Timeout:     30 * time.Second,
		Env:         execEnv(nil),
	})

	if result.TimedOut {
		t.Fatalf("plain mode hung on the approval instead of auto-denying\nstdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	}
	if result.Err != nil {
		t.Fatalf("plain run failed: %v\nstderr: %s", result.Err, result.Stderr)
	}
	if !strings.Contains(result.Stderr, "auto-denied: no terminal to approve on; approval required: re-run with --allow") {
		t.Fatalf("stderr has no auto-deny notice:\n%s", result.Stderr)
	}
	if !strings.Contains(result.Stdout, "t09-finished") {
		t.Fatalf("turn did not complete after the denial\nstdout: %s", result.Stdout)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
