package agentui

import (
	"context"
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
