package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
)

func TestMCPResponseSizeLimit(t *testing.T) {
	for _, kind := range []string{"json", "sse", "stdio"} {
		for _, size := range []int{8*1024*1024 - 1, 8 * 1024 * 1024, 8*1024*1024 + 1} {
			t.Run(fmt.Sprintf("%s/%d", kind, size), func(t *testing.T) {
				var c transport
				var err error
				if kind == "stdio" {
					t.Setenv("INVOKR_MCP_RESPONSE_SIZE", strconv.Itoa(size))
					c, err = startClient(config.MCPServerConfig{Command: os.Args[0], Args: []string{"-test.run=^TestSizedMCPFixture$"}})
				} else {
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						if kind == "sse" {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "data: %s\n\n", sizedResponse(size))
						} else {
							_, _ = fmt.Fprint(w, sizedResponse(size))
						}
					}))
					defer srv.Close()
					c, err = newHTTPClient(config.MCPServerConfig{URL: srv.URL})
				}
				if err != nil {
					t.Fatal(err)
				}
				defer c.close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				err = c.request(ctx, "tools/list", nil, nil)
				if (err != nil) != (size > 8*1024*1024) {
					t.Fatalf("size=%d error=%v", size, err)
				}
			})
		}
	}
}

func sizedResponse(size int) string {
	const body = `{"id":1,"result":{}}`
	return strings.Repeat(" ", size-len(body)) + body
}

func TestSizedMCPFixture(t *testing.T) {
	if size, _ := strconv.Atoi(os.Getenv("INVOKR_MCP_RESPONSE_SIZE")); size > 0 {
		_, _ = fmt.Fprintln(os.Stdout, sizedResponse(size))
	}
}
