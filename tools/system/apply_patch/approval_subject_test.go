package apply_patch

import (
	"testing"

	"github.com/invokr-agent/invokr/tools/system/workspace"
)

// A patch approval must carry a Subject: it is the only field standing grants
// match against, so without it a `docs/*` grant can never cover a patch.
func TestPatchApprovalCarriesPathSubject(t *testing.T) {
	changes := []plannedFileChange{
		{displayPath: "docs/one.md", action: "update"},
		{displayPath: "docs/.hidden.md", action: "update"},
	}
	req := patchApprovalRequest(changes, workspace.Policy{}, "patch", 1, false)
	if req.Subject != "docs/one.md docs/.hidden.md" {
		t.Fatalf("subject = %q", req.Subject)
	}
}

// apply_patch declares approval policy "ask", and the Approve hook is the sole
// gate for native tools — so even a patch touching only ordinary paths must
// produce an approval request (issue #325).
func TestPatchApprovalAsksForOrdinaryPaths(t *testing.T) {
	changes := []plannedFileChange{{displayPath: "pkg/ordinary.go", action: "update"}}
	req := patchApprovalRequest(changes, workspace.Policy{}, "patch", 1, false)
	if req.Reason == "" {
		t.Fatal("expected a non-empty reason for an ordinary-path patch")
	}
	if req.Tier != "write_workspace" {
		t.Fatalf("tier = %q, want write_workspace", req.Tier)
	}
}
