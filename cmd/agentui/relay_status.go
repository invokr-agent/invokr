package agentui

import (
	"context"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

const relayPollInterval = 15 * time.Second

// relayRunning is whether an agent-relay process is up, as of the last poll.
var relayRunning atomic.Bool

// relayAlive is a package var so tests need no real process table.
var relayAlive = func(ctx context.Context) bool {
	return exec.CommandContext(ctx, "pgrep", "-x", "agent-relay").Run() == nil
}

// relayStatusText answers /relay. agent-relay's own status names the directory
// it serves; no transport shares a live TUI session with it yet.
func (m *model) relayStatusText([]string) string {
	if !relayAlive(m.ctx) {
		return "chat relay: not running\nthis session: not shared"
	}
	out, err := exec.CommandContext(m.ctx, "agent-relay", "status").Output()
	if err != nil {
		return "error: agent-relay status: " + err.Error()
	}
	return strings.TrimSpace(string(out)) + "\nthis session: not shared"
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
