package clients

import (
	"testing"

	"github.com/rivt-ai/go-inference-router/router"
	routercfg "github.com/rivt-ai/go-inference-router/router/config"
)

// The router builds no installer without a trust root, and that failure is
// silent: managed installation simply switches off and unsigned binaries on
// PATH become the only way to find a provider. Since router v0.7.0 the release
// key is compiled into the library, so this asserts the default still reaches
// an installer — a regression upstream would otherwise look like nothing at all
// until a provider install fails.
func TestReleaseTrustRootBuildsAnInstaller(t *testing.T) {
	installer, err := newProviderInstaller()
	if err != nil {
		t.Fatalf("newProviderInstaller: %v", err)
	}
	if installer == nil {
		t.Fatal("no installer built; provider installation would be disabled")
	}
}

// A configured trust root makes router.PathLookupAllowed report false, which
// would strand every provider installed by the previous 'go install' path —
// those binaries are found by name on PATH and nothing migrates them. This
// asserts the concession is still in place, so removing it is a deliberate act
// with a failing test attached rather than a silent break for existing users.
func TestPathLookupSurvivesTheTrustRoot(t *testing.T) {
	if !router.PathLookupAllowed(RouterSpec{}.routerConfig()) {
		t.Fatal("PATH lookup is disabled; providers installed by the old " +
			"'go install' path would stop resolving with no migration")
	}
	// Without the concession the compiled-in release key alone disables it,
	// which is what makes this a concession rather than a no-op.
	bare := routercfg.Config{Version: routercfg.Version}
	if router.PathLookupAllowed(bare) {
		t.Fatal("a bare config no longer disables PATH lookup upstream; " +
			"the AllowPathLookup concession in registryTrust is now pointless")
	}
}
