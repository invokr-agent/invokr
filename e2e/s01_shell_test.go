//go:build e2e

package e2e

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
)

func shellCall(id, command string, args ...string) providertest.CompletionStep {
	payload := map[string]any{"command": command}
	if len(args) > 0 {
		payload["args"] = args
	}
	return providertest.NativeToolCallStep(id, "shell", payload)
}

// runShellTiers drives one scripted shell call per case and returns the
// approval requests it raised. Every approval is denied, so a demoted command
// can never run — the manual plan's "deny and verify no output leaked" step.
func runShellTiers(t *testing.T, steps []providertest.CompletionStep) (inProcResult, *providertest.Server) {
	t.Helper()
	provider := providertest.NewScripted(t, append(steps, providertest.ReplyStep("s-done"))...)
	t.Cleanup(provider.Close)
	home := newHome(t, provider.URL())
	got := runInProc(t, home, provider.URL(), "run the commands", denyAll)
	provider.AssertClean(t)
	provider.AssertExhausted(t)
	return got, provider
}

// tierOf returns the approval tier raised for subject, or "" when the call was
// classified readonly and never asked.
func tierOf(got inProcResult, subject string) string {
	for _, req := range got.Approvals {
		if req.Subject == subject {
			return req.Tier
		}
	}
	return ""
}

// TestS01Shell_TierTable covers plan case S01: the widened default allowlist
// stays readonly for in-workspace reads, and any argument that can name a file
// outside the workspace demotes the call to the write tier (approval).
func TestS01Shell_TierTable(t *testing.T) {
	t.Run("default allowlist is exactly the documented enum", func(t *testing.T) {
		want := []string{"ls", "pwd", "cat", "head", "tail", "wc", "find", "grep", "rg", "git", "go", "gofmt", "echo", "file", "od", "sort", "awk", "cut", "tee", "sha256sum", "whoami", "id", "stat", "date", "chmod", "mkdir", "cp", "rm", "rmdir", "mv", "touch", "which", "sed", "base64", "xxd", "sleep", "timeout", "tr", "uniq", "diff", "xargs"}
		got := constants.DefaultShellAllowedCommands()
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("default allowed commands = %v, want %v", got, want)
		}
	})

	t.Run("in-workspace reads are readonly and never ask", func(t *testing.T) {
		got, _ := runShellTiers(t, []providertest.CompletionStep{
			shellCall("s01-cat", "cat", "README.md"),
			shellCall("s01-head", "head", "-n", "5", "docs/architecture.md"),
			shellCall("s01-grep", "grep", "amber-orbit", "README.md"),
			shellCall("s01-wc", "wc", "-l", "README.md"),
			shellCall("s01-find", "find", ".", "-name", "*.md", "-maxdepth", "2"),
		})
		if len(got.Approvals) != 0 {
			t.Fatalf("readonly commands asked for approval: %#v", got.Approvals)
		}
		if len(got.Resp.ToolFailures) != 0 {
			t.Fatalf("readonly commands failed: %#v", got.Resp.ToolFailures)
		}
	})

	t.Run("workspace escapes demote to the write tier", func(t *testing.T) {
		escapes := map[string]providertest.CompletionStep{
			"cat /etc/passwd":              shellCall("s01-passwd", "cat", "/etc/passwd"),
			"cat ../outside.txt":           shellCall("s01-parent", "cat", "../outside.txt"),
			"wc --files0-from=/etc/passwd": shellCall("s01-files0", "wc", "--files0-from=/etc/passwd"),
			"head /tmp/x":                  shellCall("s01-tmp", "head", "/tmp/x"),
			"find . -delete":               shellCall("s01-delete", "find", ".", "-delete"),
		}
		var steps []providertest.CompletionStep
		for _, step := range escapes {
			steps = append(steps, step)
		}
		got, provider := runShellTiers(t, steps)

		for subject := range escapes {
			switch tier := tierOf(got, subject); tier {
			case constants.ShellTierWrite:
			case "":
				t.Fatalf("%q was classified readonly and ran without approval", subject)
			default:
				t.Fatalf("%q raised tier %q, want %q", subject, tier, constants.ShellTierWrite)
			}
		}
		// Denied at the write tier, so nothing outside the workspace was read.
		if len(got.Resp.ToolFailures) != len(escapes) {
			t.Fatalf("ledger = %#v, want one denial per escaping call", got.Resp.ToolFailures)
		}
		assertNoCanary(t, toolResults(provider.Snapshot().CompletionRequests))
	})
}

// TestS02Shell_CodeExecutorsNeverReadonly covers plan case S02: a command that runs
// project-supplied code is never readonly, and the ecosystem drivers are not in
// the default allowlist at all.
func TestS02Shell_CodeExecutorsNeverReadonly(t *testing.T) {
	t.Run("allowlisted executors still ask", func(t *testing.T) {
		executors := map[string]providertest.CompletionStep{
			"go test ./...":     shellCall("s02-gotest", "go", "test", "./..."),
			"go run main.go":    shellCall("s02-gorun", "go", "run", "main.go"),
			"rg --pre sh probe": shellCall("s02-pre", "rg", "--pre", "sh", "probe"),
			"rg --pre=sh probe": shellCall("s02-pre-eq", "rg", "--pre=sh", "probe"),
		}
		var steps []providertest.CompletionStep
		for _, step := range executors {
			steps = append(steps, step)
		}
		got, _ := runShellTiers(t, steps)

		for subject := range executors {
			if tier := tierOf(got, subject); tier == "" {
				t.Fatalf("%q was classified readonly", subject)
			} else if tier == constants.ShellTierReadonly {
				t.Fatalf("%q raised the readonly tier", subject)
			}
		}
	})

	t.Run("interpreters ask at the write tier", func(t *testing.T) {
		if !sandbox.Available() && os.Getenv("INVOKR_TOOLBOX_ALLOW_UNSANDBOXED") != "1" {
			t.Skip("no sandbox to route interpreters into; they are refused here")
		}
		subjects := map[string]providertest.CompletionStep{
			"python3 --version": shellCall("s02-py", "python3", "--version"),
			"node --version":    shellCall("s02-node", "node", "--version"),
		}
		var steps []providertest.CompletionStep
		for _, step := range subjects {
			steps = append(steps, step)
		}
		got, _ := runShellTiers(t, steps)
		for subject := range subjects {
			if tier := tierOf(got, subject); tier != constants.ShellTierWrite {
				t.Fatalf("%q raised tier %q, want %q", subject, tier, constants.ShellTierWrite)
			}
		}
	})

	// Interpreters (node, python3) are no longer here: they are routed into the
	// sandbox at the write tier instead of being refused. Build drivers still
	// are not on the allowlist and are refused by name.
	t.Run("ecosystem drivers are not in the default allowlist", func(t *testing.T) {
		drivers := []string{"npm", "cargo", "pytest", "make"}
		var steps []providertest.CompletionStep
		for i, driver := range drivers {
			steps = append(steps, shellCall("s02-driver-"+driver+string(rune('0'+i)), driver, "--version"))
		}
		got, _ := runShellTiers(t, steps)

		if len(got.Approvals) != 0 {
			t.Fatalf("a disallowed command reached approval: %#v", got.Approvals)
		}
		if len(got.Resp.ToolFailures) != len(drivers) {
			t.Fatalf("ledger = %#v, want one failure per disallowed driver", got.Resp.ToolFailures)
		}
		for _, failure := range got.Resp.ToolFailures {
			if !strings.Contains(failure.Detail, "is not allowed") {
				t.Fatalf("failure detail is not the allowlist rejection: %q", failure.Detail)
			}
			for _, allowed := range constants.DefaultShellAllowedCommands() {
				if !strings.Contains(failure.Detail, allowed) {
					t.Fatalf("failure detail omits allowed command %q: %q", allowed, failure.Detail)
				}
			}
		}
	})
}
