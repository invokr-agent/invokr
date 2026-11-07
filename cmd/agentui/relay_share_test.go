package agentui

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/surface/acpagent"
)

func sharePaths(t *testing.T) (socket, config string) {
	runtime, conf := t.TempDir(), t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_CONFIG_HOME", conf)
	t.Cleanup(func() { share, sharePath = nil, "" })
	return acpagent.SocketPath(), acpagent.RelayConfigPath()
}

func resetShare() { share, sharePath = nil, "" }

type shareSession struct{}

func (shareSession) SessionID() string { return "share-test" }

func (shareSession) Send(context.Context, string) (turn.AgentResponse, error) {
	return turn.AgentResponse{Message: "ok"}, nil
}

func givenHost() {
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	setShareHost(shareSession{}, broker, approval.NewApprovalQueue(context.Background(), broker), nil, context.Background(), nil)
}

// One reader per socket: a second concurrent Read lets the earlier waiter
// swallow the frames the next phase waits for.
type peerReader struct {
	mu      sync.Mutex
	waiting chan int
}

func readPeer(peer net.Conn) *peerReader {
	r := &peerReader{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _ := peer.Read(buf)
			r.mu.Lock()
			waiting := r.waiting
			r.waiting = nil
			r.mu.Unlock()
			if waiting != nil {
				waiting <- n
			}
			if n == 0 {
				return
			}
		}
	}()
	return r
}

// Frames nobody waits for are dropped, so a leftover cannot be mistaken for
// the next phase's request.
func (r *peerReader) watching() chan int {
	out := make(chan int, 1)
	r.mu.Lock()
	r.waiting = out
	r.mu.Unlock()
	return out
}

func TestShareOpenServesAndMirrors(t *testing.T) {
	socket, config := sharePaths(t)
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"version":1,"agent":["invokr","acp"],"token":"keep-me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	putRelayOnPath(t, "true")
	relayRunning.Store(true)
	t.Cleanup(func() { relayRunning.Store(false) })
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	queue := approval.NewApprovalQueue(context.Background(), broker)
	setShareHost(shareSession{}, broker, queue, nil, context.Background(), nil)
	if reply, _ := openShare(); !strings.Contains(reply, "sharing this session") {
		t.Fatalf("openShare = %q", reply)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	peer, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		t.Fatalf("dial the shared socket = %v", err)
	}
	defer func() { _ = peer.Close() }()
	dialled := readPeer(peer)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && share.Status() != "shared" {
		time.Sleep(10 * time.Millisecond)
	}
	if share.Status() != "shared" {
		t.Fatal("openShare never served a connection: nothing watches the socket")
	}

	reader := dialled.watching()
	go func() { _, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell", Subject: "ls"}) }()
	select {
	case n := <-reader:
		t.Fatalf("the host's own approval reached the chat contact (%d bytes)", n)
	case <-time.After(500 * time.Millisecond):
	}

	reader = dialled.watching()
	pinned := approval.Pin(ctx)
	go func() {
		_, _ = broker.RequestApproval(pinned, approval.Request{Tool: "shell", Subject: "git push"})
	}()
	select {
	case n := <-reader:
		if n == 0 {
			t.Fatal("the pinned request reached the peer as nothing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a pinned approval never reached the chat contact")
	}

	closeShare()
	reader = dialled.watching()
	go func() {
		_, _ = broker.RequestApproval(approval.Pin(ctx), approval.Request{Tool: "shell", Subject: "rm -rf"})
	}()
	select {
	case <-reader:
		t.Fatal("the chat contact was still asked after the share closed")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestShareOpenRefuses(t *testing.T) {
	socket, _ := sharePaths(t)
	relayRunning.Store(false)
	t.Cleanup(func() { relayRunning.Store(false) })
	if reply, _ := openShare(); !strings.Contains(reply, "invokr relay start") {
		t.Fatalf("openShare with the relay down = %q, want the hint to start it", reply)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("a socket was bound at %s while the relay is down: %v", socket, err)
	}

	relayRunning.Store(true)
	givenHost()
	holder, err := acpagent.OpenShare(socket, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("OpenShare = %v", err)
	}
	reply, _ := openShare()
	if !strings.Contains(reply, socket) || !strings.Contains(reply, "already serving a shared session") {
		t.Fatalf("openShare on a held socket = %q, want the path and the holder named", reply)
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("close holder = %v", err)
	}
}

func TestShareOpenNeedsConfiguredRelay(t *testing.T) {
	socket, config := sharePaths(t)
	relayRunning.Store(true)
	givenHost()
	t.Cleanup(func() { relayRunning.Store(false) })
	reply, _ := openShare()
	if !strings.Contains(reply, "configure agent-relay first") {
		t.Fatalf("openShare without a config = %q", reply)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("a config was written at %s: %v", config, err)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("the socket was left behind at %s: %v", socket, err)
	}
}

func TestShareOpenAndCloseRewriteOnlyTheSocket(t *testing.T) {
	socket, config := sharePaths(t)
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"version":1,"agent":["invokr","acp"],"token":"keep-me","agent_socket":"/stale"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(t.TempDir(), "args.txt")
	putRelayOnPath(t, "echo \"$@\" > "+args)
	relayRunning.Store(true)
	givenHost()
	t.Cleanup(func() { relayRunning.Store(false) })

	reply, restart := openShare()
	if !strings.Contains(reply, "sharing this session") {
		t.Fatalf("openShare = %q", reply)
	}
	if restart != nil {
		restart()
	}
	stored, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), socket) || !strings.Contains(string(stored), "keep-me") {
		t.Fatalf("config after open = %s, want the socket and the untouched answers", stored)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("no socket bound at %s: %v", socket, err)
	}
	reply, restart = closeShare()
	if !strings.Contains(reply, "stopped sharing") {
		t.Fatalf("closeShare = %q", reply)
	}
	if restart != nil {
		restart()
	}
	stored, err = os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "agent_socket") || !strings.Contains(string(stored), "keep-me") {
		t.Fatalf("config after close = %s, want agent_socket cleared and the rest kept", stored)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("close left the socket at %s: %v", socket, err)
	}
	recorded, err := os.ReadFile(args)
	if err != nil {
		t.Fatalf("the relay was never restarted: %v", err)
	}
	if !strings.Contains(string(recorded), "-replace") || !strings.Contains(string(recorded), "-cwd") {
		t.Fatalf("relay restart args = %s, want -replace and the shared directory", recorded)
	}
}

func TestShareRestartIsDeferredAndKeepsTheShare(t *testing.T) {
	socket, config := sharePaths(t)
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"agent":["invokr","acp"],"token":"keep-me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	putRelayOnPath(t, "exit 1")
	relayRunning.Store(true)
	t.Cleanup(func() { relayRunning.Store(false) })
	broker := approval.NewBroker()
	broker.Policy = approval.NewPolicy(approval.Manual, false)
	setShareHost(shareSession{}, broker, approval.NewApprovalQueue(context.Background(), broker), nil,
		context.Background(), nil)

	reply, restart := openShare()
	if !strings.Contains(reply, "sharing this session") {
		t.Fatalf("openShare = %q, want the share taken even when the restart fails", reply)
	}
	if restart == nil {
		t.Fatal("openShare restarted agent-relay inline instead of handing it back as a command")
	}
	if !strings.Contains(shareLine(), "this session: listening at "+socket) {
		t.Fatalf("a failed restart dropped the share: %q", shareLine())
	}
	stored, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(stored), socket) {
		t.Fatalf("config after a failed restart = %v", stored)
	}
	reported := restart()
	msg, isNote := reported.(shareNoteMsg)
	if !isNote {
		t.Fatalf("a failed restart reported %v, want a message for Update to show", reported)
	}
	if !strings.Contains(msg.text, "did not restart") {
		t.Fatalf("a failed restart reported %q, want the failure named", msg.text)
	}
	closeShare()
}

func TestShareCloseRestartsForTheSharedDirectory(t *testing.T) {
	_, config := sharePaths(t)
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"agent":["invokr","acp"],"token":"keep-me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(t.TempDir(), "args.txt")
	putRelayOnPath(t, "echo \"$@\" > "+args)
	relayRunning.Store(true)
	t.Cleanup(func() { relayRunning.Store(false) })
	givenHost()
	_, restart := openShare()
	if restart != nil {
		restart()
	}
	_, restart = closeShare()
	if restart == nil {
		t.Fatal("closeShare handed back no restart")
	}
	restart()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(args)
	if err != nil {
		t.Fatalf("the relay was never restarted after closing: %v", err)
	}
	if !strings.Contains(string(recorded), "-cwd "+wd) {
		t.Fatalf("restart args = %s, want -cwd for the directory the share served", recorded)
	}
}

// The sharer reports from its own goroutine: it hands the model a message so
// every chatLog touch stays on the event loop.
func TestShareNoteTravelsThroughTheEventLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := newModel(context.Background(), nil, newToolEventQueue(), nil, nil, false, nil, nil)
	inbox := make(chan shareNoteMsg, 8)
	setShareHost(shareSession{}, approval.NewBroker(), nil, nil, ctx,
		func(text string) { inbox <- shareNoteMsg{text} })
	before := m.chatLog.Len()
	go func() {
		for round := 0; round < 8; round++ {
			shareNote("a contact dropped the shared socket")
		}
	}()
	for round := 0; round < 8; round++ {
		_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
		_, _ = m.Update(<-inbox)
	}
	cancel()
	if m.chatLog.Len() <= before {
		t.Fatal("a share note never reached the transcript through Update")
	}
}

func TestShareCloseWithoutShare(t *testing.T) {
	resetShare()
	if reply, _ := closeShare(); reply != "this session does not share" {
		t.Fatalf("closeShare = %q", reply)
	}
	if line := shareLine(); line != "this session: not shared" {
		t.Fatalf("shareLine = %q", line)
	}
}
