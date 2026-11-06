package agentui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
)

const relayPollInterval = 15 * time.Second

var relayStatusTimeout = 2 * time.Second

// relayRunning is whether an agent-relay process is up, as of the last poll.
var relayRunning atomic.Bool

// relayAlive is a package var so tests need no real process table.
var relayAlive = func(ctx context.Context) bool {
	return exec.CommandContext(ctx, "pgrep", "-x", "agent-relay").Run() == nil
}

// relayStatusText answers /relay with agent-relay's own status for this
// directory. Opening or closing a share restarts agent-relay, which does not
// exit promptly, so that restart goes back as a tea.Cmd for the TUI to run after
// Update rather than inside it.
func (m *model) relayStatusText(args []string) (string, tea.Cmd) {
	action := ""
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "open":
		return openShare()
	case "close":
		return closeShare()
	}
	ctx, cancel := context.WithTimeout(m.ctx, relayStatusTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "agent-relay", "status").CombinedOutput()
	status := strings.TrimSpace(string(out))
	switch {
	case errors.Is(err, exec.ErrNotFound):
		status = "agent-relay is not installed; see `invokr relay status`"
	case status == "" && err != nil:
		status = "agent-relay status: " + err.Error()
	}
	return status + "\n" + shareLine(), nil
}

// watchRelay polls for agent-relay until ctx ends, calling changed when the
// answer flips so the header can repaint.
func watchRelay(ctx context.Context, changed func()) {
	tick := time.NewTicker(relayPollInterval)
	defer tick.Stop()
	for {
		if up := relayAlive(ctx); relayRunning.Swap(up) != up {
			changed()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
