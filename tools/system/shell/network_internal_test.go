package shell

import (
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
)

// A sandbox plan keeps the host network namespace exactly when the line is
// network tier or the operator allowed network commands outright (#543):
// an unshared netns has no DNS, so curl/wget cannot resolve anything.
func TestSandboxPlanHostNetworkWhenNetworkAllowed(t *testing.T) {
	bash, err := bashPath()
	if err != nil {
		t.Skipf("no bash: %v", err)
	}
	for _, tc := range []struct {
		name string
		plan preparedShellCommand
		want bool
	}{
		{"read-only tier, network not allowed", preparedShellCommand{line: "ls"}, false},
		{"network tier", preparedShellCommand{line: "git fetch", tier: shellTierNetwork}, true},
		{"network commands allowed", preparedShellCommand{line: "curl https://example.com", allowNetwork: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sandboxPlan(bash, tc.plan).Network; got != tc.want {
				t.Fatalf("sandboxPlan(%q).Network = %v, want %v", tc.plan.line, got, tc.want)
			}
		})
	}
}

func TestShellApprovalDescribesActualNetworkBoundary(t *testing.T) {
	if desc := shellDescription([]string{"python3"}, "", true); !strings.Contains(desc, "UNSANDBOXED") || !strings.Contains(desc, "skips shell approvals") || !strings.Contains(desc, "Allowed commands: any command") {
		t.Fatalf("YOLO description misstates execution: %q", desc)
	}
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	for _, tc := range []struct {
		line, want           string
		tier                 shellTier
		sandboxExec, allowed bool
	}{
		{"python3 script.py", "network isolated", shellTierWrite, true, false},
		{"python3 script.py", "host network available", shellTierWrite, true, true},
		{"touch output", "network isolated", shellTierWrite, false, false},
		{"git fetch", "host network available", shellTierNetwork, false, false},
	} {
		plan := preparedShellCommand{line: tc.line, tier: tc.tier, sandboxExec: tc.sandboxExec, allowNetwork: tc.allowed}
		req, err := shellApprovalRequest(config.ShellConfig{}, plan)
		if err != nil || req == nil || !strings.Contains(req.Reason, tc.want) {
			t.Fatalf("approval boundary = %+v, %v; want %q", req, err, tc.want)
		}
	}
}
