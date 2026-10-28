package agentui

import (
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/platform/version"
)

// TestVersionBadgeUpdateHint checks the header badge composition: the hint
// renders as the command that fetches the newer release, and nothing renders
// when the checker reports none.
func TestVersionBadgeUpdateHint(t *testing.T) {
	hinted := versionBadge("v0.2.0", "v0.3.0")
	if !strings.Contains(hinted, "run invokr update v0.3.0") {
		t.Fatalf("badge = %q, want the update hint for v0.3.0", hinted)
	}
	if got := versionBadge("v0.2.0", ""); got != "v0.2.0" {
		t.Fatalf("badge = %q, want the bare stamp when the checker reports none", got)
	}
}

// TestUpdateHintOffForDevAndOptOut: the checker stays off for an unstamped
// build and for INVOKR_NO_UPDATE_CHECK, so the badge stays bare in both cases
// and no lookup runs.
func TestUpdateHintOffForDevAndOptOut(t *testing.T) {
	origVersion, origCommit, origDate := version.Version, version.Commit, version.Date
	defer func() { version.Version, version.Commit, version.Date = origVersion, origCommit, origDate }()
	version.Version = "dev"
	version.Commit = "none"
	version.Date = "unknown"
	if got := buildVersion(); strings.Contains(got, "update") {
		t.Fatalf("unstamped badge = %q, want no update hint", got)
	}
	t.Setenv("INVOKR_NO_UPDATE_CHECK", "1")
	version.Version = "v0.2.0"
	version.Commit = "abc1234"
	version.Date = "2026-10-20T00:00:00Z"
	if got := buildVersion(); strings.Contains(got, "update") {
		t.Fatalf("stamped badge with the opt-out = %q, want no update hint", got)
	}
}
