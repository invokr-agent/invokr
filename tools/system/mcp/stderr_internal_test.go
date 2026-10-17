package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
)

func TestStdioStderrBoundedDuringErrors(t *testing.T) {
	t.Setenv("INVOKR_MCP_STDERR_FIXTURE", "1")
	c, err := startClient(config.MCPServerConfig{Command: os.Args[0], Args: []string{"-test.run=^TestNoisyMCPFixture$"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 10 {
		err := c.request(ctx, "tools/call", nil, nil)
		if err == nil {
			t.Fatal("missing server error")
		}
		if len(err.Error()) > 64*1024+100 {
			t.Fatalf("unbounded diagnostic: %d bytes", len(err.Error()))
		}
	}
}

func TestNoisyMCPFixture(t *testing.T) {
	if os.Getenv("INVOKR_MCP_STDERR_FIXTURE") != "1" {
		return
	}
	_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), 1<<20))
	go func() {
		for {
			if _, err := os.Stderr.Write([]byte("diagnostic\n")); err != nil {
				return
			}
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "{\"id\":%d,\"error\":{\"message\":\"failed\"}}\n", req.ID)
	}
}
