package shell

import (
	"slices"
	"testing"
)

// The go module cache mount in exec.go resolves GOMODCACHE/GOPATH/GOCACHE on
// the host, but an isolated shell only forwarded PATH/HOME/LANG/TERM/TMPDIR -
// so `go` inside the sandbox fell back to $HOME/go/pkg/mod instead of
// agreeing with what was actually mounted. These must pass through.
func TestBuildShellEnvForwardsGoCacheVars(t *testing.T) {
	t.Setenv("GOMODCACHE", "/host/gomodcache")
	t.Setenv("GOPATH", "/host/gopath")
	t.Setenv("GOCACHE", "/host/gocache")

	env := buildShellEnv(nil)
	for _, want := range []string{"GOMODCACHE=/host/gomodcache", "GOPATH=/host/gopath", "GOCACHE=/host/gocache"} {
		if !slices.Contains(env, want) {
			t.Fatalf("buildShellEnv() = %v, want it to contain %q", env, want)
		}
	}
}
