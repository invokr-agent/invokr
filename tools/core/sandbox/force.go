package sandbox

import "testing"

var forced *bool

// ForceAvailable makes Available report v until the test ends, so policy
// tests cover both branches on any host. Not safe with t.Parallel.
func ForceAvailable(t testing.TB, v bool) {
	t.Helper()
	forced = &v
	t.Cleanup(func() { forced = nil })
}
