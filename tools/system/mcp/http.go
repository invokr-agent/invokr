package mcp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/invokr-agent/invokr/config"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// httpClient speaks MCP streamable HTTP: every request is a POST of one
// JSON-RPC message, and the response is either a single JSON object or an SSE
// stream whose frames must be filtered down to the matching response id.
//
// TLS verification is deliberately not configurable — remote MCP servers are
// untrusted tool providers, so plaintext is allowed only against loopback
// (which is what the tests use).
type httpClient struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu      sync.Mutex
	nextID  int
	session string
}

func newHTTPClient(server config.MCPServerConfig) (*httpClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(server.URL))
	if err != nil {
		return nil, fmt.Errorf("parse MCP url: %w", err)
	}
	if host := parsed.Hostname(); parsed.Scheme != "https" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("MCP url must use https (got %q)", parsed.Scheme)
	}
	headers := map[string]string{}
	for header, envName := range server.HeadersEnv {
		if value := os.Getenv(envName); value != "" {
			headers[header] = value
		}
	}
	return &httpClient{url: parsed.String(), headers: headers, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *httpClient) request(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	payload, err := encodeRPC(id, method, params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("MCP %s: %w", method, err)
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("MCP %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		c.session = session
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MCP %s: HTTP %d", method, resp.StatusCode)
	}
	if strings.HasPrefix(method, "notifications/") {
		return nil
	}
	message, err := readHTTPMessage(resp, id)
	if err != nil {
		return fmt.Errorf("MCP %s: %w", method, err)
	}
	return decodeRPC(method, message, out)
}

func (c *httpClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for header, value := range c.headers {
		req.Header.Set(header, value)
	}
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
}

// readHTTPMessage returns the raw JSON-RPC message carrying the given id. For
// SSE responses that means skipping notification frames and responses to other
// requests rather than decoding the first frame that arrives.
func readHTTPMessage(resp *http.Response, id int) ([]byte, error) {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read MCP response: %w", err)
		}
		if len(body) > maxMessageBytes {
			return nil, fmt.Errorf("MCP response exceeds %d bytes", maxMessageBytes)
		}
		if !messageMatchesID(body, id) {
			return nil, fmt.Errorf("response ID does not match request %d", id)
		}
		return body, nil
	}
	return readSSEMessage(resp.Body, id)
}

func readSSEMessage(body io.Reader, id int) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageBytes+8)
	var event []byte
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(event) > 0 && messageMatchesID(event, id) {
				return event, nil
			}
			event = event[:0]
		} else if data, ok := strings.CutPrefix(line, "data:"); ok {
			event = append(event, strings.TrimPrefix(data, " ")...)
			event = append(event, '\n')
			if len(event)-1 > maxMessageBytes {
				return nil, fmt.Errorf("MCP stream event exceeds %d bytes", maxMessageBytes)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read MCP stream: %w", err)
	}
	return nil, fmt.Errorf("stream ended without a response")
}

func (c *httpClient) close() {
	c.client.CloseIdleConnections()
}
