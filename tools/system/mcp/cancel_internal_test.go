package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
)

func TestStdioCancelBlockedWrite(t *testing.T) {
	c, err := startClient(config.MCPServerConfig{Command: "sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- c.request(ctx, "tools/call", strings.Repeat("x", 1<<20), nil) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want deadline error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt blocked stdin")
	}
}
