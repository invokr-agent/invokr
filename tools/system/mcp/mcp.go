package mcp

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
)

const (
	sourcePrefix       = "mcp:"
	discoveryTimeout   = 30 * time.Second
	defaultCallTimeout = 2 * time.Minute
	maxMessageBytes    = 8 * 1024 * 1024
	maxDiscoveryPages  = 32
	maxDiscoveryTools  = 4096
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolList struct {
	Tools      []mcpTool `json:"tools"`
	NextCursor string    `json:"nextCursor"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type callResult struct {
	IsError bool `json:"isError,omitempty"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func RegisterMCPTools(registrar core.NativeRegistrar, deps core.CallerDeps, logger *zap.Logger) {
	for name, server := range deps.App.MCP.Servers {
		if server.Enabled {
			go registerServerTools(registrar, name, server, deps.Approver, logger)
		}
	}
}

func registerServerTools(registrar core.NativeRegistrar, name string, server config.MCPServerConfig, approver approval.Approver, logger *zap.Logger) {
	tools, err := loadServerTools(context.Background(), name, server)
	if err != nil {
		if server.Required {
			registerErrorTool(registrar, name, err)
		} else {
			logger.Warn("skipping optional MCP server", zap.String("server", name), zap.Error(err))
		}
		return
	}
	dynamic := core.MapSlice(filterTools(tools, server), func(tool mcpTool) core.DynamicNativeTool {
		return dynamicTool(name, server, tool, approver)
	})
	for _, skippedName := range registrar.ReplaceNativeToolsBySource(sourcePrefix+name, dynamic) {
		logger.Warn("MCP tool ignored in favor of native tool", zap.String("name", skippedName))
	}
}

func dynamicTool(serverName string, server config.MCPServerConfig, tool mcpTool, approver approval.Approver) core.DynamicNativeTool {
	name := "mcp_" + safeName(serverName) + "_" + safeName(tool.Name)
	schema := tool.InputSchema
	if len(schema) == 0 {
		schema = map[string]any{"type": "object", "additionalProperties": true}
	}
	// Remote tool descriptions are untrusted; every transport defaults to ask.
	mode := strutil.FirstNonEmpty(server.DefaultToolsApprovalMode, constants.ApprovalPolicyAsk)
	metadata := core.NativeMetadata("mcp", []string{"mcp_tool"}, mode, core.ToolCapabilities{Network: true, ExecutesCode: true})
	metadata.AlwaysVisible, metadata.ManagedApproval = false, true
	spec := core.ToolSpec{
		Name: name, Description: strutil.FirstNonEmpty(tool.Description, "MCP tool "+tool.Name),
		Example: fmt.Sprintf(`{"name":"%s","arguments":{}}`, name), Parameters: schema, Metadata: metadata,
	}
	return core.DynamicNativeTool{
		Spec: spec,
		Handler: func(ctx context.Context, call *core.ToolCall) (string, error) {
			args, err := core.DecodeOptionalToolArgs[map[string]any](call)
			if err != nil {
				return "", fmt.Errorf("parse MCP tool arguments: %w", err)
			}
			req := approval.Request{Tool: spec.Name, Tier: "mcp_tool", Summary: "Call MCP tool", Details: "Server: " + serverName + "\nTool: " + tool.Name, Subject: mcpArgsSubject(args), Network: true}
			decision := core.Ask(req)
			if mode == constants.ApprovalPolicyAuto {
				decision = core.Automatic(req)
			}
			if err := core.EnforceApproval(ctx, approver, spec, decision); err != nil {
				return "", err
			}
			return callServerTool(ctx, serverName, server, tool.Name, args)
		},
	}
}

// mcpArgsSubject is the matchable argument string for an MCP tool approval:
// a space-joined, key-sorted "k=v" rendering of the call arguments, so a
// standing grant like `mcp_fixture_echo 1 'hello*'` can match. Nothing
// beyond the arguments themselves goes here.
func mcpArgsSubject(args map[string]any) string {
	return strings.Join(core.MapSlice(slices.Sorted(maps.Keys(args)), func(k string) string { return fmt.Sprintf("%s=%v", k, args[k]) }), " ")
}

// Discovery publishes only a complete list and bounds both pages and total tools.
func loadServerTools(ctx context.Context, name string, server config.MCPServerConfig) (tools []mcpTool, err error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	client, err := persistentClient(ctx, name, server)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			evictClient(clientKey(name, server), client)
		}
	}()
	params, seen := map[string]string{}, map[string]bool{}
	for range maxDiscoveryPages {
		var page toolList
		if err := client.request(ctx, "tools/list", params, &page); err != nil {
			return nil, err
		}
		if len(tools)+len(page.Tools) > maxDiscoveryTools {
			return nil, fmt.Errorf("MCP discovery exceeds %d tools", maxDiscoveryTools)
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" {
			return tools, nil
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("MCP discovery repeated cursor")
		}
		seen[page.NextCursor], params["cursor"] = true, page.NextCursor
	}
	return nil, fmt.Errorf("MCP discovery exceeds %d pages", maxDiscoveryPages)
}

func callServerTool(ctx context.Context, name string, server config.MCPServerConfig, toolName string, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	client, err := persistentClient(ctx, name, server)
	if err != nil {
		return "", err
	}
	var result callResult
	if err := client.request(ctx, "tools/call", map[string]any{"name": toolName, "arguments": args}, &result); err != nil {
		evictClient(clientKey(name, server), client)
		return "", err
	}
	var parts []string
	for _, content := range result.Content {
		if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
			parts = append(parts, content.Text)
		}
	}
	if len(parts) == 0 {
		raw, _ := json.Marshal(result)
		parts = append(parts, string(raw))
	}
	output := strings.Join(parts, "\n")
	if result.IsError {
		return output, &core.ToolError{Code: "mcp_tool_error", Payload: output}
	}
	return output, nil
}

// transport is one MCP connection: stdio subprocess or streamable HTTP.
// request sends notifications/* without an ID or response wait.
type transport interface {
	request(ctx context.Context, method string, params any, out any) error
	close()
}

type stdioClient struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	reader     *bufio.Scanner
	stderr     *core.TruncatingWriters
	gate, done chan struct{}
	nextID     int
}

type clientSlot struct {
	ready  chan struct{}
	client transport
	err    error
}

var clientRegistry = struct {
	sync.Mutex
	clients map[string]*clientSlot
}{clients: map[string]*clientSlot{}}

func clientKey(name string, server config.MCPServerConfig) string {
	data, _ := json.Marshal(server)
	return name + "\x00" + string(data)
}

func persistentClient(ctx context.Context, name string, server config.MCPServerConfig) (transport, error) {
	key := clientKey(name, server)
	clientRegistry.Lock()
	if slot := clientRegistry.clients[key]; slot != nil {
		clientRegistry.Unlock()
		select {
		case <-slot.ready:
			return slot.client, slot.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	slot := &clientSlot{ready: make(chan struct{})}
	clientRegistry.clients[key] = slot
	clientRegistry.Unlock()

	slot.client, slot.err = startInitializedClient(ctx, server)
	close(slot.ready)
	if slot.err != nil {
		clientRegistry.Lock()
		if clientRegistry.clients[key] == slot {
			delete(clientRegistry.clients, key)
		}
		clientRegistry.Unlock()
	}
	return slot.client, slot.err
}

func startInitializedClient(ctx context.Context, server config.MCPServerConfig) (transport, error) {
	client, err := startTransport(server)
	if err != nil {
		return nil, err
	}
	const version = "2024-11-05"
	params := map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "invokr", "version": "0"}}
	var result struct{ ProtocolVersion string }
	err = client.request(ctx, "initialize", params, &result)
	if err == nil && result.ProtocolVersion != version {
		err = fmt.Errorf("unsupported MCP protocol version %q", result.ProtocolVersion)
	}
	if err == nil {
		err = client.request(ctx, "notifications/initialized", nil, nil)
	}
	if err != nil {
		client.close()
		return nil, err
	}
	return client, nil
}

// startTransport picks the transport from config: a URL means streamable HTTP,
// otherwise a stdio subprocess. Validation guarantees exactly one is set, but
// config can also be built in-process, so both are still checked here.
func startTransport(server config.MCPServerConfig) (transport, error) {
	if strings.TrimSpace(server.URL) != "" {
		if strings.TrimSpace(server.Command) != "" {
			return nil, fmt.Errorf("MCP server sets both command and url")
		}
		return newHTTPClient(server)
	}
	return startClient(server)
}

func startClient(server config.MCPServerConfig) (*stdioClient, error) {
	if strings.TrimSpace(server.Command) == "" {
		return nil, fmt.Errorf("MCP command is required")
	}
	cmd := exec.CommandContext(context.Background(), server.Command, server.Args...)
	core.ConfigureProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := core.NewTruncatingWriters(64 * 1024)
	cmd.Stderr = stderr.Stderr()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server: %w", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	reader := bufio.NewScanner(stdout)
	reader.Buffer(make([]byte, 4096), maxMessageBytes+2)
	client := &stdioClient{cmd: cmd, stdin: stdin, stdout: stdout, reader: reader, stderr: stderr, gate: make(chan struct{}, 1), done: done}
	client.gate <- struct{}{}
	return client, nil
}

func (c *stdioClient) request(ctx context.Context, method string, params any, out any) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer func() { c.gate <- struct{}{} }()
	c.nextID++
	payload, err := encodeRPC(c.nextID, method, params)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- c.exchange(method, payload, out) }()
	select {
	case err := <-done:
		return c.withStderr(err)
	case <-ctx.Done():
		c.kill()
		<-done //nolint:errcheck // Finish pipe I/O and decoding before releasing the gate.
		return fmt.Errorf("MCP %s: %w", method, ctx.Err())
	}
}

func (c *stdioClient) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.withStderr(fmt.Errorf("MCP server exited"))
	case <-c.gate:
		return nil
	}
}

// exchange runs under the request gate; cancellation closes its pipes and drains it.
func (c *stdioClient) exchange(method string, payload []byte, out any) error {
	if _, err := fmt.Fprintf(c.stdin, "%s\n", payload); err != nil {
		return fmt.Errorf("write MCP request: %w", err)
	}
	if strings.HasPrefix(method, "notifications/") {
		return nil
	}
	for c.reader.Scan() {
		message := c.reader.Bytes()
		if len(message) > maxMessageBytes {
			return fmt.Errorf("read MCP message: MCP response exceeds %d bytes", maxMessageBytes)
		}
		if messageMatchesID(message, c.nextID) {
			return decodeRPC(method, message, out)
		}
	}
	return fmt.Errorf("read MCP message: %w", cmp.Or(c.reader.Err(), io.EOF))
}

// messageMatchesID reports whether a raw JSON-RPC message carries the given
// response id. Notifications (no id field) decode to id 0, which never
// matches a real request id (ids start at 1), so they're naturally skipped.
// A message that fails to parse at all is treated as a match so the caller
// still surfaces the parse error instead of hanging forever.
func messageMatchesID(message []byte, id int) bool {
	var probe struct{ ID int }
	return json.Unmarshal(message, &probe) != nil || probe.ID == id
}

func encodeRPC(id int, method string, params any) ([]byte, error) {
	if strings.HasPrefix(method, "notifications/") {
		id = 0
	}
	return json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
}

func decodeRPC(method string, respBytes []byte, out any) error {
	var resp rpcResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return fmt.Errorf("parse MCP response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("MCP %s failed: %s", method, resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

func (c *stdioClient) withStderr(err error) error {
	if detail := strings.TrimSpace(c.stderr.StderrString()); err != nil && detail != "" {
		return fmt.Errorf("%w; stderr: %s", err, detail)
	}
	return err
}

// Lifecycle methods only receive clients returned by a successful startClient.
func (c *stdioClient) close() {
	c.kill()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
}

func (c *stdioClient) kill() {
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	_ = c.cmd.Cancel()
}

func evictClient(key string, failed transport) {
	clientRegistry.Lock()
	slot := clientRegistry.clients[key]
	if slot != nil && slot.client == failed {
		delete(clientRegistry.clients, key)
	}
	clientRegistry.Unlock()
	if slot != nil && slot.client == failed {
		failed.close()
	}
}

// ShutdownClients terminates all persistent MCP subprocesses.
func ShutdownClients() {
	clientRegistry.Lock()
	all := clientRegistry.clients
	clientRegistry.clients = map[string]*clientSlot{}
	clientRegistry.Unlock()
	for _, slot := range all {
		<-slot.ready
		if slot.client != nil {
			slot.client.close()
		}
	}
}

func filterTools(tools []mcpTool, server config.MCPServerConfig) []mcpTool {
	enabled, disabled := core.StringSet(server.EnabledTools), core.StringSet(server.DisabledTools)
	out := slices.DeleteFunc(slices.Clone(tools), func(tool mcpTool) bool {
		_, allowed := enabled[tool.Name]
		_, denied := disabled[tool.Name]
		return denied || (len(enabled) > 0 && !allowed)
	})
	slices.SortFunc(out, func(a, b mcpTool) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func registerErrorTool(registrar core.NativeRegistrar, server string, loadErr error) {
	core.Register(registrar, core.Tool[map[string]any]{
		Spec: core.ToolSpec{
			Name: "mcp_" + safeName(server) + "_error", Description: "Reports MCP server load failure.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false},
			Metadata:   core.NativeMetadata("mcp", []string{"mcp_tool"}, constants.ApprovalPolicyAuto, core.ToolCapabilities{}),
		},
		Execute: func(context.Context, map[string]any) (string, error) { return "", loadErr },
	})
}

func safeName(name string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}), "_")
}
