package tmux

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
	"github.com/invokr-ai/invokr/tools/system/shell"
)

// sandboxTmp is where the sandbox mounts the shared tmp dir, so the socket and
// the server config the holder names live under it.
const sandboxTmp = "/tmp"

// holderPID is the last holder started, for the integration test.
var holderPID int

// openSandboxed starts the tmux server inside a namespace sandbox — the holder
// whose namespaces every pane it later spawns inherits — and returns once the
// session exists. Host-side clients reach the server over the shared socket.
func openSandboxed(ctx context.Context, plan preparedTmux) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("resolve tmux: %w", err)
	}
	tmpDir := fsutil.SharedTmpDir(plan.root)
	conf := createServerConf(tmpDir)
	cmd, unlink, err := sandbox.Command(ctx, sandboxOpenPlan(tmuxPath, plan, tmpDir), plan.cwd, []string{"-c", holdLine(conf)})
	if err != nil {
		_ = os.Remove(conf)
		return fmt.Errorf("build tmux sandbox: %w", err)
	}
	cleanup := func() { unlink(); _ = os.Remove(conf) }
	log, err := os.CreateTemp(tmpDir, "tmux-holder-*.log")
	if err != nil {
		cleanup()
		return fmt.Errorf("create tmux holder log: %w", err)
	}
	// Read only when the session never appears; the holder keeps its own fd.
	defer func() { _ = log.Close(); _ = os.Remove(log.Name()) }()
	// The same environment the shell tool gives model-supplied code: the
	// server and every pane must not see the agent's secrets.
	cmd.Env = append(shell.IsolatedEnv(), "SHELL="+paneShell())
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		cleanup()
		return fmt.Errorf("start sandboxed tmux server: %w", err)
	}
	holderPID = cmd.Process.Pid
	go func() { _ = cmd.Wait(); cleanup() }()
	if err := awaitSession(ctx, plan.root, holderSession); err != nil {
		if ctx.Err() == nil { // a holder that never published is a leak; a cancelled call is not
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		out, _ := os.ReadFile(log.Name())
		return fmt.Errorf("sandboxed tmux server did not start: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// holdLine starts the server on the holder session and then blocks the
// namespace's pid 1 on a wait-for channel nobody signals: `new-session -d`
// forks the server and exits, and an exiting pid 1 would take the namespace —
// and the server — with it. No model text goes in the line, so nothing the
// model can pkill -f matches the server's argv (#528).
func holdLine(conf string) string {
	sock := filepath.Join(sandboxTmp, socketName+".sock")
	base := []string{"tmux", "-S", sock}
	if conf != "" {
		base = append(base, "-f", filepath.Join(sandboxTmp, filepath.Base(conf)))
	}
	base = append(base, "new-session", "-d", "-s", holderSession)
	return strings.Join(base, " ") + " && exec tmux -S " + sock + " wait-for invokr-hold"
}

// sandboxOpenPlan mirrors the shell tool's mounts. Network is deliberately the
// host's: a service in a pane must stay reachable from network-tier shell
// calls and from the user's own browser.
func sandboxOpenPlan(tmuxPath string, plan preparedTmux, tmpDir string) sandbox.Plan {
	sb := sandbox.BuildPlan(tmuxPath, "", "", plan.root, core.ToolCapabilities{WritesWorkspace: true, Network: true}, 0, nil)
	if plan.mode == approval.Auto {
		sb.Sealed = plan.protected
	}
	sb.RO = append(sb.RO, "/etc/alternatives", "/etc/passwd", "/etc/group", filepath.Dir(paneShell()))
	sb.RO = append(sb.RO, shell.UserBinDirs()...)
	if _, inside := fsutil.RelInside(plan.root, plan.cwd); !inside {
		sb.RW = append(sb.RW, plan.cwd)
	}
	sb.TmpDir, sb.Detached, sb.CommandPath = tmpDir, true, "/bin/sh" // the holder is a shell line
	return sandbox.DedupPlan(sb)
}

// paneShell is the shell tmux gives a pane.
func paneShell() string { return cmp.Or(os.Getenv("SHELL"), "/bin/sh") }

// awaitSession polls until the sandboxed server has published the session, so
// open returns a name the very next call can address.
func awaitSession(ctx context.Context, root, session string) error {
	for range 40 {
		if _, err := runTmux(ctx, root, "has-session", "-t", session); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("timed out")
}
