package agentui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
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
// directory. It runs inside Update, so a hung agent-relay must not freeze the
// UI; the status exits non-zero when the relay is down, and its text says so.
func (m *model) relayStatusText([]string) string {
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
	return status + "\nthis session: not shared"
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
