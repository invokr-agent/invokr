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

// /relay shows agent-relay's own status, whatever state it reports, and says
// this session is not shared. Not installed, a status that fails with its own
// reason and one that hangs are each answered rather than swallowed.
func TestRelayCommandReportsAgentRelayStatus(t *testing.T) {
	t.Cleanup(func() { relayStatusTimeout = 2 * time.Second })
	relayStatusTimeout = 200 * time.Millisecond
	for _, c := range []struct{ name, script, want string }{
		{"running", "echo 'relay running in /home/me/proj'", "relay running in /home/me/proj"},
		{"down", "echo 'agent-relay is not running'; exit 1", "agent-relay is not running"},
		{"failure reason", "echo 'no status for this directory' >&2; exit 3", "no status for this directory"},
		{"hung", "while :; do :; done", "agent-relay status: signal: killed"},
		{"not installed", "", "agent-relay is not installed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.script == "" {
				t.Setenv("PATH", t.TempDir())
			} else {
				putRelayOnPath(t, c.script)
			}
			m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
			if _, handled := m.runCommand("/relay"); !handled {
				t.Fatal("/relay not handled")
			}
			if reply := m.chatLog.messages[len(m.chatLog.messages)-1].text; !strings.HasPrefix(reply, c.want) || !strings.HasSuffix(reply, "this session: not shared") {
				t.Fatalf("/relay reply = %q, want %q then the shared line", reply, c.want)
			}
		})
	}
}
