// Anyone who can write the socket may dial it; the peer check and per-turn pin are the guard.

package agentui

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/surface/acpagent"

	tea "charm.land/bubbletea/v2"
)

// A chat contact gets this TUI's session, broker and bus, so it cannot switch cwd or mode.
var hostSession turn.IdentifiedSession
var hostApprovals *approval.Broker
var hostBus events.Bus
var hostCtx context.Context

var share *acpagent.Sharer
var sharePath string
var shareWd string
var shareQueue *approval.ApprovalQueue
var shareStop func()
var shareNote func(string)

// agent-relay does not exit promptly, so a restart belongs in a tea.Cmd rather
// than in Update, and it is bounded so a hung relay cannot hold the quit path.
const relayRestartWait = 15 * time.Second

// The watcher's error callback and the restart command run off the event loop,
// so neither may touch the model.
type shareNoteMsg struct {
	text string
}

func setShareHost(session turn.Session, approvals *approval.Broker, queue *approval.ApprovalQueue, bus events.Bus,
	ctx context.Context, note func(string)) {
	hostSession, _ = session.(turn.IdentifiedSession)
	hostApprovals, hostBus = approvals, bus
	shareQueue, hostCtx, shareNote = queue, ctx, note
}

func openShare() (string, tea.Cmd) {
	if share != nil {
		return shareLine(), nil
	}
	if !relayRunning.Load() {
		return "agent-relay is not running: start it with `invokr relay start`, then /relay open", nil
	}
	if hostSession == nil || hostApprovals == nil {
		return "this session cannot share: it carries no session identity or approval broker", nil
	}
	path := acpagent.SocketPath()
	next, err := acpagent.OpenShare(path, hostBus, hostSession, hostApprovals, nil)
	if err != nil {
		return err.Error(), nil
	}
	ws, err := os.Getwd()
	if err != nil {
		_ = next.Close()
		return err.Error(), nil
	}
	if err := acpagent.SetRelaySocket(path); err != nil {
		_ = next.Close()
		return err.Error(), nil
	}
	// Take the share before restarting the relay: a restart that fails leaves
	// the socket listening and agent_socket pointing at it, and this session
	// still owns both, so /relay close can put them back.
	ctx, stop := context.WithCancel(hostCtx)
	share, sharePath, shareWd, shareStop = next, path, ws, stop
	go next.Watch(ctx, func(err error) {
		shareNote("a connection to the shared socket failed: " + err.Error())
	})
	if shareQueue != nil {
		// A bound chat turn parks its approvals in this queue, so the queue has
		// to offer them to the chat contact as well as the panel.
		shareQueue.SetMirror(next.Mirror(shareQueue.Decide), next.Settled)
	}
	return "sharing this session at " + path +
			"\nagent-relay now serves " + ws + ", dialling it instead of starting a new `invokr acp`;\n" +
			"chat turns answer for their own approvals: this session's mode and grants do not apply to them",
		restartCmd(ws)
}

// shareChanged keeps chat contacts' status current after the panel changes mode, model or effort.
func shareChanged() {
	if share != nil {
		share.Changed()
	}
}

func closeShare() (string, tea.Cmd) {
	if share == nil {
		return "this session does not share", nil
	}
	path, ws, sharer, stop := sharePath, shareWd, share, shareStop
	// Clear the mirror first: a chat contact must not answer anything the
	// host parks after the socket is gone.
	if shareQueue != nil {
		shareQueue.SetMirror(nil, nil)
	}
	if stop != nil {
		stop()
	}
	// Clear the share before the close can fail: a share whose socket resists
	// closing would otherwise outlive the session that can no longer serve it.
	share, sharePath, shareWd, shareStop = nil, "", "", nil
	closed := sharer.Close()
	if err := acpagent.SetRelaySocket(""); err != nil {
		return "the socket at " + path + " is closed, but agent_socket is still set: " + err.Error(), nil
	}
	if closed != nil {
		return "the socket at " + path + " did not close cleanly: " + closed.Error(), nil
	}
	return "stopped sharing: " + path + " removed, agent-relay back on `invokr acp`", restartCmd(ws)
}

// restartCmd hands the relay restart to the TUI to run after Update.
func restartCmd(ws string) tea.Cmd {
	return func() tea.Msg {
		if err := restartRelay(ws); err != nil {
			return shareNoteMsg{"agent-relay did not restart: " + err.Error() +
				"\nrun `invokr relay start --replace` to point it where this session needs"}
		}
		return nil
	}
}

// agent-relay reads agent_socket only at start and reconnect, so restart it.
// The directory goes on both paths: the relay serves that tree, and a restart
// without it would move the shared session's chats to wherever the relay
// happens to run.
func restartRelay(ws string) error {
	bin, err := exec.LookPath("agent-relay")
	if err != nil {
		return err
	}
	args := []string{"start", "-replace", "-cwd", ws}
	ctx, cancel := context.WithTimeout(context.Background(), relayRestartWait)
	defer cancel()
	return exec.CommandContext(ctx, bin, args...).Run()
}

func shareLine() string {
	if share == nil {
		return "this session: not shared"
	}
	return "this session: " + share.Status() + " at " + sharePath
}
