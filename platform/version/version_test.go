package version

import "testing"

func TestStringFormatsVersionCommitAndDate(t *testing.T) {
	origVersion, origCommit, origDate := Version, Commit, Date
	defer func() { Version, Commit, Date = origVersion, origCommit, origDate }()

	Version, Commit, Date = "v0.1.0", "abc1234", "2026-08-07T00:00:00Z"

	got := String()
	want := "v0.1.0 (commit abc1234, built 2026-08-07T00:00:00Z)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestStringDefaultsToDevOutsideReleaseBuild(t *testing.T) {
	if Version != "dev" || Commit != "none" || Date != "unknown" {
		t.Fatalf("unexpected package defaults: version=%q commit=%q date=%q", Version, Commit, Date)
	}
	got := String()
	want := "dev (commit none, built unknown)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
