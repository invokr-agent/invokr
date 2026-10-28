package docker_sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/invokr-agent/invokr/support/fsutil"
)

// dockerfile is written verbatim into the temp build context.
// Base image is digest-pinned (fetched via `docker inspect --format='{{index .RepoDigests 0}}'
// debian:bookworm-slim`) so builds are reproducible and immune to tag mutation/poisoning.
// apt packages are left unpinned: Debian snapshot pinning (snapshot.debian.org) was considered
// but skipped as unnecessary churn for this small, low-risk package set.
const dockerfile = `FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818

RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-venv git tmux sqlite3 util-linux ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY invokr /invokr
RUN chmod +x /invokr

ENTRYPOINT ["/invokr"]
`

// ensureImage returns a docker image tag suitable for running the current
// invokr binary. If no image with the tag exists it builds one.
// Auto-build is only supported on Linux hosts.
func ensureImage(engine string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("sandbox: auto-build is only supported on Linux hosts (current GOOS=%s); "+
			"set sandbox.image to a pre-built linux/amd64 image", runtime.GOOS)
	}

	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("sandbox: cannot resolve executable path: %w", err)
	}

	binHash, err := hashFile(exePath)
	if err != nil {
		return "", fmt.Errorf("sandbox: cannot hash binary: %w", err)
	}

	tag := "invokr:sandbox-" + binHash

	inspectOut, inspectErr := exec.Command(engine, "image", "inspect", "--format", "{{.Id}}", tag).Output() //nolint:gosec
	if inspectErr == nil && len(inspectOut) > 0 {
		return tag, nil
	}

	tmpDir, err := os.MkdirTemp("", "invokr-sandbox-*")
	if err != nil {
		return "", fmt.Errorf("sandbox: cannot create build context: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := stageBuildContext(exePath, tmpDir); err != nil {
		return "", err
	}

	buildCmd := exec.Command(engine, "build", "-t", tag, tmpDir) //nolint:gosec
	buildCmd.Stderr = os.Stderr
	buildCmd.Stdout = io.Discard
	if err := buildCmd.Run(); err != nil {
		return "", fmt.Errorf("sandbox: image build failed: %w", err)
	}

	return tag, nil
}

// stageBuildContext copies the running binary into tmpDir as "invokr" and
// writes the Dockerfile that references it.
func stageBuildContext(exePath, tmpDir string) error {
	if err := fsutil.CopyFile(exePath, filepath.Join(tmpDir, "invokr"), 0o755); err != nil {
		return fmt.Errorf("sandbox: cannot copy binary to build context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		return fmt.Errorf("sandbox: cannot write Dockerfile: %w", err)
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}
