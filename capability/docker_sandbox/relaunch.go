// Package docker_sandbox relaunches the whole agent process inside a
// Docker/podman container (MaybeRelaunch, buildDockerArgs, ensureImage).
//
// This is distinct from the per-subprocess namespace/rlimit isolation
// applied to individual toolbox tools in tools/system/toolbox/sandbox_linux.go.
package docker_sandbox

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/invokr-ai/invokr/config"
)

// containerInvokrHome is where the host's invokr home dir is mounted inside
// the sandbox container (the container runs as root).
const containerInvokrHome = "/root/.invokr"

// MaybeRelaunch checks whether the process should re-exec itself inside a
// container. It returns (true, exitCode, nil) when the re-exec ran to
// completion; (true, 0, err) when sandbox is requested but setup failed;
// (false, 0, nil) when sandbox is disabled or we are already inside one.
func MaybeRelaunch(cfg config.SandboxConfig, baseURL string, rawArgs []string) (handled bool, exitCode int, err error) {
	if os.Getenv("INVOKR_IN_SANDBOX") == "1" {
		// Whole-agent Docker sandbox and the per-toolbox unshare namespace sandbox
		// are alternatives, not layers (see ADR 0005): running inside the
		// container disables the inner sandbox, so make that visible at startup.
		if os.Getenv("INVOKR_TOOLBOX_ALLOW_UNSANDBOXED") == "1" {
			fmt.Fprintln(os.Stderr, "toolbox namespace sandbox disabled inside whole-agent sandbox")
		}
		return false, 0, nil
	}
	if rawArgDisablesSandbox(rawArgs) {
		return false, 0, nil
	}
	if !cfg.Enabled && !rawArgRequiresSandbox(rawArgs) {
		return false, 0, nil
	}

	if _, lookErr := exec.LookPath(cfg.Engine); lookErr != nil {
		return true, 1, fmt.Errorf(
			"sandbox: %q binary not found in PATH — install docker or set sandbox.engine to a different container CLI",
			cfg.Engine)
	}

	image := cfg.Image
	if image == "" {
		image, err = ensureImage(cfg.Engine)
		if err != nil {
			return true, 1, err
		}
	}

	workspaceRoot, err := resolveWorkspaceRoot()
	if err != nil {
		return true, 1, fmt.Errorf("sandbox: cannot resolve workspace root: %w", err)
	}

	dockerArgs := buildDockerArgs(cfg.Engine, image, workspaceRoot, config.InvokrHomeDir(), rawArgs[1:],
		term.IsTerminal(os.Stdin.Fd()), detectSELinux(), baseURL)

	// dockerArgs[0] is cfg.Engine (an operator-configured binary, not user/network
	// input); the remaining args come from buildDockerArgs, built from trusted
	// config/env values rather than arbitrary external input.
	cmd := exec.Command(dockerArgs[0], dockerArgs[1:]...) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	runErr := cmd.Run()
	if runErr == nil {
		return true, 0, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
		return true, exitErr.ExitCode(), nil
	}
	return true, 1, fmt.Errorf("sandbox: container run failed (is the docker daemon running?): %w", runErr)
}

func resolveWorkspaceRoot() (string, error) {
	root := os.Getenv("INVOKR_WORKSPACE_ROOT")
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root = wd
	}
	return filepath.Abs(root)
}

func detectSELinux() bool {
	if err := exec.Command("selinuxenabled").Run(); err == nil { //nolint:gosec
		return true
	}
	_, err := os.Stat("/sys/fs/selinux")
	return err == nil
}

func rawArgRequiresSandbox(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool {
		return a == "--sandbox" || a == "--sandbox=true" || a == "--sandbox=1"
	})
}

func rawArgDisablesSandbox(args []string) bool {
	for i, a := range args {
		if a == "--sandbox=false" || a == "--sandbox=0" ||
			(a == "--sandbox" && i+1 < len(args) && (args[i+1] == "false" || args[i+1] == "0")) {
			return true
		}
	}
	return false
}

func buildDockerArgs(engine, image, workspaceRoot, invokrHome string, agentArgs []string, isTTY, selinux bool, baseURL string) []string {
	args := []string{engine, "run"}
	if isTTY {
		args = append(args, "-it")
	}
	args = append(args, "--rm", "--init")

	workVol := workspaceRoot + ":" + workspaceRoot
	homeVol := invokrHome + ":" + containerInvokrHome
	if selinux {
		workVol += ":Z"
		homeVol += ":Z"
	}
	args = append(args, "-v", workVol, "-v", homeVol, "-w", workspaceRoot)
	args = append(args, filterEnv(os.Environ())...)
	// The host invokr home is mounted at containerInvokrHome, but host-side
	// INVOKR_HOME/INVOKR_MEMORY_DIR (and any config memory.dir) still point at
	// host paths that do not exist in the container. Pin both to the mount so
	// memory written outside the sandbox is still readable inside it.
	args = append(args, "-e", "INVOKR_IN_SANDBOX=1", "-e", "INVOKR_HOME="+containerInvokrHome,
		"-e", "INVOKR_MEMORY_DIR="+containerInvokrHome, "-e", "INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1")
	args = append(args, hostLoopbackArgs(baseURL)...)
	return append(append(args, image), agentArgs...)
}

// hostLoopbackArgs makes a loopback provider base_url reachable from inside the
// container. When base_url points at localhost/127.0.0.1, the container's own
// loopback is empty, so we map host.docker.internal to the host gateway and
// rewrite the URL to it via an injected INVOKR_BASE_URL. Any other host is
// left untouched.
func hostLoopbackArgs(baseURL string) []string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
	default:
		return nil
	}
	const gateway = "host.docker.internal"
	if port := u.Port(); port != "" {
		u.Host = gateway + ":" + port
	} else {
		u.Host = gateway
	}
	return []string{"--add-host", gateway + ":host-gateway", "-e", "INVOKR_BASE_URL=" + u.String()}
}

func filterEnv(environ []string) []string {
	var out []string
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if shouldPassEnv(key) {
			out = append(out, "-e", entry)
		}
	}
	return out
}

func shouldPassEnv(key string) bool {
	switch key {
	case "INVOKR_IN_SANDBOX", "INVOKR_TOOLBOX_ALLOW_UNSANDBOXED",
		// Host-absolute paths; re-pinned to the container mount in buildDockerArgs.
		"INVOKR_HOME", "INVOKR_MEMORY_DIR":
		return false
	case "OPENAI_API_KEY", "TERM", "COLORTERM", "NO_COLOR":
		return true
	}
	return strings.HasPrefix(key, "INVOKR_")
}
