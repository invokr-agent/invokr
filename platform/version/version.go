// Package version holds the build-time identity of the invokr binary.
// Version, Commit, and Date are overridden via -ldflags -X on release builds
// (see .goreleaser.yaml and the Makefile's install target); a `go run`/
// `go build` without ldflags keeps the "dev" defaults below.
package version

import "fmt"

var (
	// Version is the release tag (e.g. "v0.1.0"), or "dev" outside a release build.
	Version = "dev"
	// Commit is the short git commit SHA the binary was built from.
	Commit = "none"
	// Date is the UTC build timestamp.
	Date = "unknown"
)

// String renders the one-line version string shown by `invokr --version`.
func String() string { return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date) }
