// Package installer runs the release installer bundled into the binary. The
// same script is published for first-time installs (see README's curl line), so
// upgrades and fresh installs share one tested code path.
package installer

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed install.sh
var script string

// Run downloads and installs a release over the running binary. An empty
// version means the newest release.
//
// The script is fed on stdin rather than written to a temp file, so there is no
// intermediate artifact to clean up or to have its permissions tampered with.
func Run(ctx context.Context, version string, out io.Writer) error {
	dir, err := installDir()
	if err != nil {
		return err
	}
	// install.sh's own writability fallback is inert once INVOKR_INSTALL_DIR is
	// set, so the check has to happen here to produce an error worth reading.
	// It runs before any download starts, so a permissions problem costs the
	// user a failed download on a slow mirror.
	if err := checkWritable(dir); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout = out
	cmd.Stderr = out
	// The version travels by env, not argv: invokr's install.sh reads
	// INVOKR_INSTALL_VERSION and takes no positional arguments. "latest" is
	// spelled out here so a stray INVOKR_INSTALL_VERSION in the caller's
	// environment cannot decide which release lands over this binary.
	cmd.Env = append(os.Environ(), "INVOKR_INSTALL_DIR="+dir, "INVOKR_INSTALL_VERSION="+orLatest(version))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install failed: %w", err)
	}
	return nil
}

// installDir identifies the running binary so an upgrade keeps the directory
// its release was installed into, whatever that was: the default ~/.local/bin,
// or a directory the user chose when they installed.
func installDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Resolve symlinks so a linked binary upgrades its target, not the link.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// Only the directory is carried over: install.sh always installs a binary
	// named invokr, which is the name this command ran under for a stamped
	// build (a dev build is refused before Run is reached).
	return filepath.Dir(exe), nil
}

// orLatest spells out the script's default instead of leaving it unset, which
// is what keeps an inherited INVOKR_INSTALL_VERSION from being read.
func orLatest(version string) string {
	if version == "" {
		// An empty version means the newest release.
		return "latest"
	}
	return version
}

func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".invokr-upgrade-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: re-run with sudo, or set INVOKR_INSTALL_DIR to a writable directory: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}
