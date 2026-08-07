package toolbox

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestEvictDaemon_OnlyEvictsMatchingPointer verifies that evictDaemon does not
// kill or delete a daemon that was already replaced by a concurrent restart.
func TestEvictDaemon_OnlyEvictsMatchingPointer(t *testing.T) {
	key := daemonKey{pluginDir: "/fake/plugin/dir", sessionID: "s1"}

	stale := &daemonProcess{}
	fresh := &daemonProcess{}

	// Seed the registry with the fresh daemon (simulating a concurrent restart
	// that already replaced the stale one before evictDaemon was called).
	daemonsMu.Lock()
	daemons[key] = fresh
	daemonsMu.Unlock()
	defer func() {
		daemonsMu.Lock()
		delete(daemons, key)
		daemonsMu.Unlock()
	}()

	// Evicting the stale pointer should be a no-op because daemons[key] != stale.
	evictDaemon(key, stale)

	daemonsMu.Lock()
	got, ok := daemons[key]
	daemonsMu.Unlock()

	if !ok {
		t.Fatal("fresh daemon was unexpectedly deleted by evictDaemon with stale pointer")
	}
	if got != fresh {
		t.Fatalf("expected fresh daemon to survive eviction, got %p", got)
	}
}

func TestDaemonRegistryIsScopedBySession(t *testing.T) {
	d1 := &daemonProcess{}
	d2 := &daemonProcess{}
	k1 := daemonKey{pluginDir: "/tool", sessionID: "one"}
	k2 := daemonKey{pluginDir: "/tool", sessionID: "two"}
	daemonsMu.Lock()
	daemons[k1], daemons[k2] = d1, d2
	daemonsMu.Unlock()
	t.Cleanup(func() { daemonsMu.Lock(); delete(daemons, k1); delete(daemons, k2); daemonsMu.Unlock() })
	if daemons[k1] == daemons[k2] {
		t.Fatal("sessions unexpectedly share daemon")
	}
}

func TestDaemonSystemReusesProcessPerSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	ShutdownDaemons()
	t.Cleanup(ShutdownDaemons)

	starts := 0
	build := func() (*exec.Cmd, func(), error) {
		starts++
		return exec.Command("sh", "-c", `count=0; while IFS= read -r line; do count=$((count + 1)); printf '{"result":"%s:%s"}\n' "$$" "$count"; done`), nil, nil
	}
	invoke := func(dp *daemonProcess) string {
		t.Helper()
		raw, err := invokeDaemon(context.Background(), dp, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		result, err := parseDaemonResponse("counter", raw)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	one, err := getDaemon(daemonKey{pluginDir: "/counter", sessionID: "one"}, 1024, build, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	first, second := invoke(one), invoke(one)
	again, err := getDaemon(daemonKey{pluginDir: "/counter", sessionID: "one"}, 1024, build, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if again != one || second != strings.TrimSuffix(first, ":1")+":2" {
		t.Fatalf("same-session daemon was not reused: first=%q second=%q", first, second)
	}

	two, err := getDaemon(daemonKey{pluginDir: "/counter", sessionID: "two"}, 1024, build, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	third := invoke(two)
	if two == one || !strings.HasSuffix(third, ":1") || strings.TrimSuffix(third, ":1") == strings.TrimSuffix(first, ":1") || starts != 2 {
		t.Fatalf("session isolation failed: first=%q third=%q starts=%d", first, third, starts)
	}

	ShutdownDaemons()
	for _, dp := range []*daemonProcess{one, two} {
		select {
		case <-dp.done:
		case <-time.After(time.Second):
			t.Fatal("daemon survived shutdown")
		}
	}
}

func TestStartDaemonCapturesBoundedStderrTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	build := func() (*exec.Cmd, func(), error) {
		return exec.Command("sh", "-c", `printf 'old-'; printf 'new-detail' >&2`), func() {}, nil
	}
	dp, err := startDaemon(6, build, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	<-dp.done
	if got := dp.stderr.String(); got != "detail" {
		t.Fatalf("stderr tail = %q", got)
	}
}

func TestTailWriterReportsOriginalWriteLength(t *testing.T) {
	w := &tailWriter{limit: 3}
	n, err := io.Copy(w, bytes.NewBufferString("abcdef"))
	if err != nil || n != 6 || !strings.HasSuffix(w.String(), "def") {
		t.Fatalf("n=%d err=%v tail=%q", n, err, w.String())
	}
}

func TestTailWriterRetainsTailOfLargeWrite(t *testing.T) {
	w := &tailWriter{limit: 3}
	if n, err := w.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := w.String(); got != "def" {
		t.Fatalf("tail = %q", got)
	}
}

func TestStartDaemonUsesDefaultOutputLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dp, err := startDaemon(0, func() (*exec.Cmd, func(), error) {
		return exec.Command("sh", "-c", "true"), nil, nil
	}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	<-dp.done
	if dp.stderr.limit <= 1 {
		t.Fatalf("unexpected stderr limit %d", dp.stderr.limit)
	}
}

func TestStartDaemonPreservesBuiltCommandArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dp, err := startDaemon(64, func() (*exec.Cmd, func(), error) {
		return exec.Command("sh", "-c", "true", "daemon-argument"), nil, nil
	}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { <-dp.done }()
	if got, want := strings.Join(dp.cmd.Args, " "), "sh -c true daemon-argument"; got != want {
		t.Fatalf("command args = %q, want %q", got, want)
	}
}

func TestSafeSessionDirIsStableAndOpaque(t *testing.T) {
	one := safeStateDir("session", "customer/session one")
	if one != safeStateDir("session", "customer/session one") {
		t.Fatal("session directory is unstable")
	}
	if one == safeStateDir("session", "customer/session two") {
		t.Fatal("different sessions share state directory")
	}
	if strings.Contains(one, "customer") || strings.Contains(one, "/") {
		t.Fatalf("session ID leaked into path %q", one)
	}
}

// TestEvictDaemon_EvictsFailedDaemon verifies that evictDaemon removes the
// entry when the failed pointer matches the currently-registered daemon.
func TestEvictDaemon_EvictsFailedDaemon(t *testing.T) {
	key := daemonKey{pluginDir: "/fake/plugin/dir2", sessionID: "s1"}

	failed := &daemonProcess{}

	daemonsMu.Lock()
	daemons[key] = failed
	daemonsMu.Unlock()
	defer func() {
		daemonsMu.Lock()
		delete(daemons, key)
		daemonsMu.Unlock()
	}()

	evictDaemon(key, failed)

	daemonsMu.Lock()
	_, ok := daemons[key]
	daemonsMu.Unlock()

	if ok {
		t.Fatal("expected failed daemon to be evicted, but it remained in the registry")
	}
}
