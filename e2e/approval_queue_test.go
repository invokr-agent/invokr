//go:build e2e

package e2e

import (
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// twoShellStep returns a single assistant response that issues two shell tool
// calls at once. Each needs write approval, so both land in the batch approval
// queue concurrently (the runner fans parallel calls out to one goroutine each).
func twoShellStep() providertest.CompletionStep {
	call := func(id, echo string) providertest.CompletionToolCall {
		return providertest.CompletionToolCall{
			ID:   id,
			Type: "function",
			Function: providertest.CompletionToolFunction{
				Name:      "shell",
				Arguments: map[string]any{"command": "sh", "args": []string{"-c", "echo " + echo}},
			},
		}
	}
	return providertest.CompletionStep{ToolCalls: []providertest.CompletionToolCall{
		call("q-one", "queue-one"),
		call("q-two", "queue-two"),
	}}
}

var queuePending = regexp.MustCompile(`APPROVAL\s+step \d+ of 2|approval queue \(2 pending\)`)

// TestApprovalQueueApproveAll drives the TUI until two approvals are queued,
// presses A (approve-all), and confirms both calls ran and the turn completed.
func TestApprovalQueueApproveAll(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("BLOCKED: tmux not installed")
	}
	provider := providertest.NewScripted(t,
		twoShellStep(),
		providertest.ReplyStep("both approved and done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	rewriteConfig(t, &home, func(app *config.AppConfig) { app.Shell.AllowedCommands = []string{"sh"} })
	s := newTUITmux(t, home, provider.URL())

	s.waitFor(regexp.MustCompile(`agent started`), 10*time.Second)
	s.submit("run two commands")
	s.waitFor(queuePending, 5*time.Second)

	s.send("A") // approve-all
	s.waitFor(regexp.MustCompile(`both approved and done`), 5*time.Second)

	s.quit(5 * time.Second)
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

// TestApprovalQueueDenyAll drives the TUI to the same two-pending state, presses
// D (deny-all), and confirms both are denied and the turn still completes.
func TestApprovalQueueDenyAll(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("BLOCKED: tmux not installed")
	}
	provider := providertest.NewScripted(t,
		twoShellStep(),
		providertest.ReplyStep("both denied and done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	rewriteConfig(t, &home, func(app *config.AppConfig) { app.Shell.AllowedCommands = []string{"sh"} })
	s := newTUITmux(t, home, provider.URL())

	s.waitFor(regexp.MustCompile(`agent started`), 10*time.Second)
	s.submit("run two commands")
	s.waitFor(queuePending, 5*time.Second)

	s.send("D") // deny-all
	s.waitFor(regexp.MustCompile(`denied all 2 pending requests`), 5*time.Second)
	s.waitFor(regexp.MustCompile(`both denied and done`), 5*time.Second)

	s.quit(5 * time.Second)
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
