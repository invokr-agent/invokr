package tmux

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/fsutil"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

// The socket and config are named at their in-sandbox paths, not the host tmp
// path; pid 1 stays blocked after the session is created; and no model text
// reaches the line, so the server's argv holds nothing pkill -f can match.
func TestHoldLineKeepsTheNamespaceAlive(t *testing.T) {
	line := holdLine("/tmp/invokr-tmp-x/c.conf")
	sock := "/tmp/" + socketName + ".sock"
	want := "tmux -S " + sock + " -f /tmp/c.conf new-session -d -s " + holderSession + " && exec tmux -S " + sock + " wait-for invokr-hold"
	if line != want {
		t.Fatalf("hold line:\n got %q\nwant %q", line, want)
	}
}

func TestSandboxOpenPlanMountsSocketDirAndSharesTheHostNetwork(t *testing.T) {
	root := t.TempDir()
	tmp := fsutil.SharedTmpDir(root)
	sb := sandboxOpenPlan("/usr/bin/tmux", preparedTmux{cwd: root, root: root}, tmp)
	if !sb.Network || !sb.Detached || sb.TmpDir != tmp || sb.CommandPath != "/bin/sh" {
		t.Fatalf("unexpected plan: network=%v detached=%v tmp=%q cmd=%q", sb.Network, sb.Detached, sb.TmpDir, sb.CommandPath)
	}
	if !slices.Contains(sb.RW, root) || !slices.Contains(sb.RO, "/etc/passwd") {
		t.Fatalf("workspace or account files not mounted: rw=%v ro=%v", sb.RW, sb.RO)
	}
}

// The pane runs in the holder's namespaces, not the agent's; it sees none of
// the agent's env; and the holder — and so the namespace — outlives the
// model's visible session, ending only with the server (#528).
func TestOpenSandboxedRunsThePaneInItsOwnNamespace(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandboxing unavailable")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("INVOKR_TEST_SECRET", "leak")
	root := t.TempDir()
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", SocketPath(root), "kill-server").Run() })
	call := &core.ToolCall{Name: "tmux", Arguments: json.RawMessage(`{"action":"open","command":"env > env.txt; sleep 30"}`)}
	plan, err := prepareTmux(call, workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	out, err := openSession(context.Background(), plan)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if ns(t, holderPID) == ns(t, os.Getpid()) {
		t.Fatalf("holder %d shares the test's mount namespace", holderPID)
	}
	var env []byte
	for range 40 { // the pane is spawned by the server, so give it a moment to write
		if env, _ = os.ReadFile(filepath.Join(root, "env.txt")); len(env) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(env), "PATH=") || strings.Contains(string(env), "INVOKR_TEST_SECRET") {
		t.Fatalf("pane env is not the isolated shell env: %q", env)
	}
	if _, err := killSession(context.Background(), preparedTmux{args: tmuxArgs{Session: result["session"]}, root: root}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// The hidden holder session keeps the server — and this process — alive
	// past the model's last visible session (#528); only kill-server ends it.
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Readlink(nsPath(holderPID)); err != nil {
		t.Fatalf("holder %d died with the last visible session: %v", holderPID, err)
	}
	_ = exec.Command("tmux", "-S", SocketPath(root), "kill-server").Run()
	for range 40 {
		if _, err := os.Readlink(nsPath(holderPID)); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("holder %d survived kill-server", holderPID)
}

func nsPath(pid int) string { return filepath.Join("/proc", strconv.Itoa(pid), "ns", "mnt") }

func ns(t *testing.T, pid int) string {
	t.Helper()
	link, err := os.Readlink(nsPath(pid))
	if err != nil {
		t.Fatalf("read mount namespace of %d: %v", pid, err)
	}
	return link
}
