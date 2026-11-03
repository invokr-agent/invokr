package agentui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWatchRelayFlagsChangesAndHeaderShowsPill(t *testing.T) {
	up := make(chan bool, 1)
	up <- true
	orig := relayAlive
	relayAlive = func(context.Context) bool { return <-up }
	t.Cleanup(func() { relayAlive = orig; relayRunning.Store(false) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 1)
	go watchRelay(ctx, func() { changed <- struct{}{} })
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported")
	}
	if !strings.Contains(renderIntro(120, 40), "relay ●") {
		t.Fatal("header has no relay pill while the relay runs")
	}
	relayRunning.Store(false)
	if strings.Contains(renderIntro(120, 40), "relay ●") {
		t.Fatal("header shows the relay pill while it is down")
	}
}

// putRelayOnPath puts a script named agent-relay on PATH, so /relay's status
// reply can be asserted without the real chat relay installed.
func putRelayOnPath(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent-relay"), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("write fake agent-relay: %v", err)
	}
	t.Setenv("PATH", dir)
}

// keepRelayAlive pins the liveness probe, then puts the real probe back.
func keepRelayAlive(t *testing.T, up bool) {
	t.Helper()
	orig := relayAlive
	t.Cleanup(func() { relayAlive = orig })
	relayAlive = func(context.Context) bool { return up }
}

// /relay answers all three questions at once: running or not, the directory the
// relay serves, and whether this session is the one chat reaches. agent-relay's
// own status text carries the directory, so it is shown verbatim.
func TestRelayCommandShowsRunningRelayAndItsDirectory(t *testing.T) {
	keepRelayAlive(t, true)
	putRelayOnPath(t, "printf '%s\\n' 'relay running in /home/me/proj for telegram'")
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	if _, handled := m.runCommand("/relay"); !handled {
		t.Fatal("/relay not handled")
	}
	reply := m.chatLog.messages[len(m.chatLog.messages)-1].text
	for _, want := range []string{"/home/me/proj", "not shared"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("/relay reply = %q, want %q", reply, want)
		}
	}
}

// A relay that is down is reported as such, without asking agent-relay.
func TestRelayCommandReportsARelayThatIsDown(t *testing.T) {
	keepRelayAlive(t, false)
	t.Setenv("PATH", t.TempDir())
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	if _, handled := m.runCommand("/relay"); !handled {
		t.Fatal("/relay not handled")
	}
	reply := m.chatLog.messages[len(m.chatLog.messages)-1].text
	if !strings.HasPrefix(reply, "chat relay: not running") {
		t.Fatalf("/relay reply = %q, want not running", reply)
	}
}

// agent-relay can be up and still refuse to report: its status command may fail.
// The failure is shown, never dressed up as "not running".
func TestRelayCommandSurfacesStatusFailure(t *testing.T) {
	keepRelayAlive(t, true)
	putRelayOnPath(t, "echo 'no status for this directory' >&2; exit 3")
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	if _, handled := m.runCommand("/relay"); !handled {
		t.Fatal("/relay not handled")
	}
	got := m.chatLog.messages[len(m.chatLog.messages)-1].text
	if !strings.HasPrefix(got, "error: ") {
		t.Fatalf("/relay reply = %q, want the failure surfaced", got)
	}
}
