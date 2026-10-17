package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

type discoveryTransport struct {
	requestPage func(context.Context, string) (string, error)
	calls       int
}

func (c *discoveryTransport) close() {}
func (c *discoveryTransport) request(ctx context.Context, method string, params any, out any) error {
	c.calls++
	if method != "tools/list" {
		return fmt.Errorf("unexpected method %s", method)
	}
	raw, err := c.requestPage(ctx, params.(map[string]string)["cursor"])
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), out)
}

func TestDiscoveryPaginatesBeforeFiltering(t *testing.T) {
	client := &discoveryTransport{requestPage: func(_ context.Context, cursor string) (string, error) {
		switch cursor {
		case "":
			return `{"tools":[{"name":"first"}],"nextCursor":"opaque cursor+/="}`, nil
		case "opaque cursor+/=":
			return `{"tools":[{"name":"later"},{"name":"denied"}],"nextCursor":"empty"}`, nil
		case "empty":
			return `{"tools":[]}`, nil
		default:
			return "", fmt.Errorf("unexpected cursor %q", cursor)
		}
	}}
	got, err := discoverTools(context.Background(), client)
	if err != nil || len(got) != 3 || client.calls != 3 {
		t.Fatalf("tools=%v calls=%d err=%v", got, client.calls, err)
	}
	filtered := filterTools(got, config.MCPServerConfig{EnabledTools: []string{" later ", " denied ", " "}, DisabledTools: []string{" denied "}})
	if len(filtered) != 1 || filtered[0].Name != "later" {
		t.Fatalf("later-page filtering: %v", filtered)
	}
}

func TestDiscoveryRejectsIncompleteLists(t *testing.T) {
	for _, tc := range []struct {
		name  string
		page  func(int) (string, error)
		want  string
		calls int
	}{
		{"repeated cursor", func(int) (string, error) { return `{"tools":[{"name":"partial"}],"nextCursor":"again"}`, nil }, "repeated cursor", 2},
		{"page limit", func(i int) (string, error) { return fmt.Sprintf(`{"tools":[],"nextCursor":"%d"}`, i), nil }, "pages", maxDiscoveryPages},
		{"second page fails", func(i int) (string, error) {
			if i == 1 {
				return `{"tools":[{"name":"partial"}],"nextCursor":"next"}`, nil
			}
			return "", errors.New("server failed")
		}, "server failed", 2},
		{"malformed page", func(int) (string, error) { return `{"tools":42}`, nil }, "unmarshal", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var client discoveryTransport
			client.requestPage = func(context.Context, string) (string, error) { return tc.page(client.calls) }
			got, err := discoverTools(context.Background(), &client)
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.want) || client.calls != tc.calls {
				t.Fatalf("tools=%v calls=%d err=%v", got, client.calls, err)
			}
		})
	}
}

func TestDiscoveryAggregateToolBoundary(t *testing.T) {
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			first, _ := json.Marshal(toolList{Tools: make([]mcpTool, maxDiscoveryTools-1), NextCursor: "next"})
			second, _ := json.Marshal(toolList{Tools: make([]mcpTool, 1+delta)})
			client := &discoveryTransport{requestPage: func(_ context.Context, cursor string) (string, error) {
				if cursor == "" {
					return string(first), nil
				}
				return string(second), nil
			}}
			got, err := discoverTools(context.Background(), client)
			if delta > 0 {
				if got != nil || err == nil || !strings.Contains(err.Error(), "tools") {
					t.Fatalf("oversized discovery accepted: err=%v", err)
				}
			} else if err != nil || len(got) != maxDiscoveryTools+delta {
				t.Fatalf("within-limit discovery rejected: len=%d err=%v", len(got), err)
			}
		})
	}
}

func TestDiscoveryPageBoundaryAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var client discoveryTransport
	client.requestPage = func(ctx context.Context, _ string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if client.calls == maxDiscoveryPages {
			return `{"tools":[{"name":"last"}]}`, nil
		}
		return fmt.Sprintf(`{"tools":[],"nextCursor":"%d"}`, client.calls), nil
	}
	got, err := discoverTools(ctx, &client)
	if err != nil || len(got) != 1 || client.calls != maxDiscoveryPages {
		t.Fatalf("last allowed page: tools=%v calls=%d err=%v", got, client.calls, err)
	}
	client.calls = 0
	client.requestPage = func(ctx context.Context, _ string) (string, error) {
		if client.calls == 1 {
			cancel()
			return `{"tools":[{"name":"partial"}],"nextCursor":"next"}`, nil
		}
		return "", ctx.Err()
	}
	if got, err = discoverTools(ctx, &client); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: tools=%v err=%v", got, err)
	}
}

// Inject the transport at the existing registry seam to exercise discovery's
// real deadline/eviction owner without launching a subprocess or remote server.
func discoverTools(ctx context.Context, client transport) ([]mcpTool, error) {
	const name = "discovery-fixture"
	server := config.MCPServerConfig{}
	key := clientKey(name, server)
	ready := make(chan struct{})
	close(ready)
	clientRegistry.Lock()
	clientRegistry.clients[key] = &clientSlot{ready: ready, client: client}
	clientRegistry.Unlock()
	defer evictClient(key, client)
	return loadServerTools(ctx, name, server)
}
