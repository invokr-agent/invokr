package agentui

import (
	"context"
	"os/exec"
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
