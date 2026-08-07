//go:build e2e

package e2e

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/clitest"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestX03_LogFilterScopesAgentSession covers plan case X03.4: the logger named
// in --log-filter's own help example must exist. It previously named nothing,
// so the filter silenced every line instead of scoping one subsystem.
func TestX03_LogFilterScopesAgentSession(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("x03-read", "read", map[string]any{"paths": []string{"README.md"}}),
		providertest.ReplyStep("x03-done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())

	result := clitest.Run(t, clitest.Options{
		State: home.state,
		CommandArgs: []string{
			"--base-url=" + provider.URL(),
			"--log-output=stderr",
			"--log-filter=agent.session=debug",
			"exec", "--json", "read the readme",
		},
		Env: execEnv(nil),
	})
	if result.Err != nil {
		t.Fatalf("exec failed: %v\nstderr: %s", result.Err, result.Stderr)
	}
	if !strings.Contains(result.Stderr, "agent.session") {
		t.Fatalf("--log-filter agent.session=debug emitted no agent.session lines:\n%s", result.Stderr)
	}
	if !strings.Contains(result.Stderr, "DEBUG") {
		t.Fatalf("filter did not raise the named logger to debug:\n%s", result.Stderr)
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
