package shell

import "testing"

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
