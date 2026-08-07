package clients

import (
	"context"
	"fmt"

	"github.com/rivt-ai/go-inference-router/router"
	routercfg "github.com/rivt-ai/go-inference-router/router/config"
	"github.com/rivt-ai/go-inference-router/router/install"
)

// InstallPlan describes what installing a provider would download, so a caller
// can show it before agreeing to it.
type InstallPlan = install.Plan

// registryTrust is the registry configuration every router entry point here
// shares. The trust root itself — the Ed25519 key signing the provider
// registry — is router.ReleasePublicKey, compiled into the library since
// v0.7.0, so nothing needs to be named here for installs to be verified.
//
// AllowPathLookup is deliberate, and it is a migration concession rather than a
// preference. A configured trust root makes router.PathLookupAllowed report
// false, which would strand every provider a user installed with the previous
// 'go install' path: those binaries live in GOBIN and are found by name on PATH,
// and nothing would migrate them. Resolution consults the verified install cache
// first, so a reinstalled provider always wins; PATH is only the fallback for one
// that has not been reinstalled yet.
//
// The cost is that an unsigned binary named go-inference-router-provider-* on
// PATH still runs. Removing this line closes that hole and is the right end
// state — after users have had a release to re-run 'invokr provider install'.
func registryTrust() routercfg.Registry { return routercfg.Registry{AllowPathLookup: true} }

// InstallProvider downloads a provider process binary and verifies it against
// the signed registry, calling confirm with what it is about to fetch.
//
// Returning an error from confirm abandons the install and downloads nothing;
// the plan is created first precisely so the size and digest can be shown
// before any bytes are transferred.
//
// No Go toolchain is involved. The manifest is checked with Ed25519, the
// download is bounded by the size the manifest names and hashed as it streams,
// and the recorded digest is checked again before the binary is ever launched.
func InstallProvider(ctx context.Context, providerType string, confirm func(InstallPlan) error) (string, error) {
	installer, err := newProviderInstaller()
	if err != nil {
		return "", err
	}
	// An empty version selects the newest stable release: the registry
	// describes the current release's artifacts only, so pinning an older
	// version resolves to nothing. Compatibility is carried by the artifact's
	// protocol field, which the router matches against the protocol it speaks.
	path, err := installer.Install(ctx, providerType, "", func(plan install.Plan) (bool, error) {
		if confirm == nil {
			return true, nil
		}
		if err := confirm(plan); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("install provider %s: %w", providerType, err)
	}
	return path, nil
}

// newProviderInstaller builds an installer over the release registry. It goes
// through router.NewInstaller rather than install.New so the cache directory
// matches the one the driver reads; two entry points computing that path
// separately would install to one place and look in another.
func newProviderInstaller() (*install.Installer, error) {
	installer, err := router.NewInstaller(
		routercfg.Config{Version: routercfg.Version, Registry: registryTrust()}, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("build provider installer: %w", err)
	}
	if installer == nil {
		return nil, fmt.Errorf("provider installer unavailable: registry public key is not configured")
	}
	return installer, nil
}
