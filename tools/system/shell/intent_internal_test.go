package shell

import (
	"testing"

	"github.com/invokr-ai/invokr/config"
)

func TestShellIntentBecomesTheApprovalSummary(t *testing.T) {
	plan := preparedShellCommand{line: "rm -rf build", tier: shellTierWrite, reasons: []string{"write command"}}
	req, err := shellApprovalRequest(config.ShellConfig{}, plan)
	if err != nil {
		t.Fatalf("shellApprovalRequest returned error: %v", err)
	}
	if req.Summary != "Run rm -rf build" {
		t.Fatalf("summary = %q, want the fallback", req.Summary)
	}
	plan.args.Intent = "clear the stale build output"
	req, err = shellApprovalRequest(config.ShellConfig{}, plan)
	if err != nil {
		t.Fatalf("shellApprovalRequest returned error: %v", err)
	}
	if req.Summary != "clear the stale build output" {
		t.Fatalf("summary = %q, want the stated intent", req.Summary)
	}
	if req.Subject != plan.line {
		t.Fatalf("subject = %q, want the literal command", req.Subject)
	}
}
