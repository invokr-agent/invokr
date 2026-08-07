//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/tools/core"
)

func TestDedupExistingPathsFiltersMissingAndDuplicates(t *testing.T) {
	existing := t.TempDir()
	missing := filepath.Join(existing, "does-not-exist")

	got := dedupExistingPaths([]string{existing, existing + "/", missing})
	if len(got) != 1 || got[0] != filepath.Clean(existing) {
		t.Fatalf("expected only the existing path deduped, got %#v", got)
	}
}

func TestBuildSandboxPlanReadWriteWorkspace(t *testing.T) {
	toolDir := t.TempDir()
	workspaceDir := t.TempDir()

	plan := BuildPlan("/bin/true", toolDir, "", workspaceDir, core.ToolCapabilities{WritesWorkspace: true}, 0, nil)

	if !containsPath(plan.RW, workspaceDir) {
		t.Fatalf("expected workspace dir in RW, got %#v", plan.RW)
	}
	if containsPath(plan.RO, workspaceDir) {
		t.Fatalf("expected workspace dir not duplicated in RO, got %#v", plan.RO)
	}
	if !containsPath(plan.RO, toolDir) {
		t.Fatalf("expected toolDir in RO, got %#v", plan.RO)
	}
}

func TestBuildSandboxPlanReadOnlyWorkspace(t *testing.T) {
	toolDir := t.TempDir()
	workspaceDir := t.TempDir()

	plan := BuildPlan("/bin/true", toolDir, "", workspaceDir, core.ToolCapabilities{ReadsWorkspace: true}, 0, nil)

	if !containsPath(plan.RO, workspaceDir) {
		t.Fatalf("expected workspace dir in RO, got %#v", plan.RO)
	}
	if containsPath(plan.RW, workspaceDir) {
		t.Fatalf("expected workspace dir not in RW, got %#v", plan.RW)
	}
}

func TestBuildSandboxPlanDoesNotExposeHostEtc(t *testing.T) {
	plan := BuildPlan("/bin/true", t.TempDir(), "", "", core.ToolCapabilities{}, 0, nil)
	if containsPath(plan.RO, "/etc") || containsPath(plan.RW, "/etc") {
		t.Fatalf("sandbox plan exposes host /etc: %#v", plan)
	}
}

func TestBuildSandboxPlanExposesNetworkConfigWithoutPasswd(t *testing.T) {
	plan := BuildPlan("/bin/true", t.TempDir(), "", "", core.ToolCapabilities{Network: true}, 0, nil)
	for _, path := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/services"} {
		if _, err := os.Stat(path); err == nil && !containsPath(plan.RO, path) {
			t.Fatalf("expected Network config %q in RO, got %#v", path, plan.RO)
		}
	}
	if containsPath(plan.RO, "/etc/passwd") {
		t.Fatalf("sandbox plan exposes /etc/passwd: %#v", plan.RO)
	}
}

// Without the trust store mounted, DNS and TCP work but every https call dies
// in the TLS handshake with curl exit 77 (#551).
func TestBuildSandboxPlanMountsCATrustStore(t *testing.T) {
	plan := BuildPlan("/bin/true", t.TempDir(), "", "", core.ToolCapabilities{Network: true}, 0, nil)
	for _, path := range caCertDirs() {
		if _, err := os.Stat(path); err == nil && !containsPath(plan.RO, path) {
			t.Fatalf("expected CA trust dir %q in RO, got %#v", path, plan.RO)
		}
	}
	offline := BuildPlan("/bin/true", t.TempDir(), "", "", core.ToolCapabilities{}, 0, nil)
	if containsPath(offline.RO, "/etc/ssl") {
		t.Fatalf("no-network plan mounts the trust store: %#v", offline.RO)
	}
}

func TestBuildSandboxPlanWithAllowHostsDropsResolvConf(t *testing.T) {
	plan := BuildPlan("/bin/true", t.TempDir(), "", "", core.ToolCapabilities{Network: true}, 0, []string{"example.test"})
	if containsPath(plan.RO, "/etc/resolv.conf") || containsPath(plan.RO, "/etc/hosts") {
		t.Fatalf("scoped network still binds the host resolver config: %#v", plan.RO)
	}
}

func TestSandboxAllowHostsGeneratesScopedHostsFile(t *testing.T) {
	if !Available() {
		t.Skip("unshare not available")
	}
	toolDir := t.TempDir()
	plan := BuildPlan("/bin/cat", toolDir, "", "", core.ToolCapabilities{Network: true}, 0, []string{"localhost"})
	cmd, cleanup, err := Command(context.Background(), plan, toolDir, []string{"/etc/hosts"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("read sandbox /etc/hosts: %v", err)
	}
	if !bytes.Contains(out, []byte("127.0.0.1 localhost")) {
		t.Fatalf("generated hosts file missing the allowed host: %q", out)
	}
}

func containsPath(paths []string, target string) bool {
	clean := filepath.Clean(target)
	for _, p := range paths {
		if p == clean {
			return true
		}
	}
	return false
}

func TestSandboxCommandExecutesToolDirCommand(t *testing.T) {
	if !Available() {
		t.Skip("unshare not available")
	}

	toolDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolDir, "marker.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	plan := BuildPlan("/bin/cat", toolDir, "", "", core.ToolCapabilities{}, 0, nil)
	cmd, cleanup, err := Command(context.Background(), plan, toolDir, []string{"marker.txt"})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sandboxed command failed: %v, stderr=%s", err, stderr.String())
	}
	if out.String() != "hello" {
		t.Fatalf("expected sandboxed cat to read tool dir file, got %q", out.String())
	}
}

func TestSandboxCommandDeniesHostRootEscape(t *testing.T) {
	if !Available() {
		t.Skip("unshare not available")
	}

	toolDir := t.TempDir()
	plan := BuildPlan("/bin/ls", toolDir, "", "", core.ToolCapabilities{}, 0, nil)
	cmd, cleanup, err := Command(context.Background(), plan, toolDir, []string{"/root"})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected sandboxed access to unbound host path to fail, stdout=%q", out.String())
	}
}

func TestSandboxCommandDeniesHostPasswd(t *testing.T) {
	if !Available() {
		t.Skip("unshare not available")
	}

	toolDir := t.TempDir()
	plan := BuildPlan("/bin/cat", toolDir, "", "", core.ToolCapabilities{}, 0, nil)
	cmd, cleanup, err := Command(context.Background(), plan, toolDir, []string{"/etc/passwd"})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected /etc/passwd read to fail, output=%q", output)
	}
}

func TestSandboxCommandTimeoutKillsChildProcessGroup(t *testing.T) {
	if !Available() {
		t.Skip("unshare not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	toolDir := t.TempDir()
	plan := BuildPlan("/bin/sleep", toolDir, "", "", core.ToolCapabilities{}, 0, nil)
	cmd, cleanup, err := Command(ctx, plan, toolDir, []string{"5"})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	started := time.Now()
	if err := cmd.Run(); err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("sandbox child survived cancellation for %v", elapsed)
	}
}

func TestSandboxCommandCancelIgnoresNonPositivePID(t *testing.T) {
	toolDir := t.TempDir()
	plan := BuildPlan("/bin/true", toolDir, "", "", core.ToolCapabilities{}, 0, nil)
	cmd, cleanup, err := Command(context.Background(), plan, toolDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd.Process = &os.Process{Pid: 0}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
}

// sandboxOrphanEnv puts the test binary into helper mode: it starts a long
// sleep through the sandbox and then blocks, so the parent can SIGKILL it.
const sandboxOrphanEnv = "INVOKR_SANDBOX_ORPHAN_HELPER"

// sandboxOrphanMarker is the sleep duration used to find the grandchild on the
// host: unique enough that no unrelated sleep matches it.
const sandboxOrphanMarker = "31337"

func TestSandboxHelperSpawnsSleep(t *testing.T) {
	if os.Getenv(sandboxOrphanEnv) == "" {
		t.Skip("helper mode only")
	}
	toolDir := t.TempDir()
	plan := BuildPlan("/bin/sleep", toolDir, "", "", core.ToolCapabilities{}, 0, nil)
	cmd, _, err := Command(context.Background(), plan, toolDir, []string{sandboxOrphanMarker})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	select {}
}

func TestSandboxChildDiesWithExternallyKilledParent(t *testing.T) {
	if !Available() {
		t.Skip("unshare not available")
	}
	helper := exec.Command(os.Args[0], "-test.run=TestSandboxHelperSpawnsSleep", "-test.v")
	helper.Env = append(os.Environ(), sandboxOrphanEnv+"=1")
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = helper.Process.Kill()
		_ = helper.Wait()
	}()

	if !waitForSleepMarker(true) {
		t.Fatal("sandboxed sleep never started")
	}
	if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = helper.Wait()
	if !waitForSleepMarker(false) {
		t.Fatal("sandboxed sleep survived an external SIGKILL of its parent")
	}
}

// waitForSleepMarker polls up to 2s for the marker sleep to be present (want
// true) or gone (want false).
func waitForSleepMarker(want bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sleepMarkerRunning() == want {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return sleepMarkerRunning() == want
}

func sleepMarkerRunning() bool {
	out, _ := exec.Command("pgrep", "-f", "^/bin/sleep "+sandboxOrphanMarker+"$").Output()
	return strings.TrimSpace(string(out)) != ""
}

func TestBuildSandboxPlanEnablesLoopbackByDefault(t *testing.T) {
	if !loopbackEnabled() {
		t.Fatalf("expected loopback enabled by default")
	}

	t.Setenv("INVOKR_SANDBOX_LOOPBACK", "0")
	if loopbackEnabled() {
		t.Fatal("expected INVOKR_SANDBOX_LOOPBACK=0 to disable loopback")
	}
}

func TestSandboxScriptBringsLoopbackUpOnlyWithoutNetwork(t *testing.T) {
	for _, tc := range []struct {
		name           string
		plan           Plan
		loopbackOff    bool
		wantLoopbackUp bool
	}{
		{"no network, loopback on", Plan{CommandPath: "/bin/true"}, false, true},
		{"no network, loopback off", Plan{CommandPath: "/bin/true"}, true, false},
		{"network on", Plan{CommandPath: "/bin/true", Network: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.loopbackOff {
				t.Setenv("INVOKR_SANDBOX_LOOPBACK", "0")
			}
			script, cleanup, err := writeSandboxScript(tc.plan, t.TempDir())
			if err != nil {
				t.Fatalf("writeSandboxScript: %v", err)
			}
			defer cleanup()
			body, err := os.ReadFile(script)
			if err != nil {
				t.Fatalf("read script: %v", err)
			}
			if got := strings.Contains(string(body), "ip link set lo up"); got != tc.wantLoopbackUp {
				t.Fatalf("loopback-up present=%v, want %v; script:\n%s", got, tc.wantLoopbackUp, body)
			}
		})
	}
}
