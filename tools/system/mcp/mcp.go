package mcp

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/tools/core"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	sourcePrefix       = "mcp:"
	discoveryTimeout   = 30 * time.Second
	defaultCallTimeout = 2 * time.Minute
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
	Tools []mcpTool `json:"tools"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func RegisterMCPTools(registrar core.NativeRegistrar, deps core.CallerDeps, logger *zap.Logger) {
	for name, server := range deps.App.MCP.Servers {
		if !server.Enabled {
			continue
		}
		go registerServerTools(registrar, name, server, deps.Approver, logger)
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
	dynamic := make([]core.DynamicNativeTool, 0, len(tools))
	for _, tool := range filterTools(tools, server) {
		dynamic = append(dynamic, dynamicTool(name, server, tool, approver))
	}
	skipped := registrar.ReplaceNativeToolsBySource(sourcePrefix+name, dynamic)
	for _, skippedName := range skipped {
		logger.Warn("MCP tool ignored in favor of native tool", zap.String("name", skippedName))
	}
}

func dynamicTool(serverName string, server config.MCPServerConfig, tool mcpTool, approver approval.Approver) core.DynamicNativeTool {
	name := "mcp_" + safeName(serverName) + "_" + safeName(tool.Name)
	schema := tool.InputSchema
	if len(schema) == 0 {
		schema = map[string]any{"type": "object", "additionalProperties": true}
	}
	metadata := core.NativeMetadata("mcp", []string{"mcp_tool"}, approvalMode(server), core.ToolCapabilities{Network: true, ExecutesCode: true})
	metadata.AlwaysVisible = false
	metadata.ManagedApproval = true
	spec := core.ToolSpec{
		Name: name, Description: core.FirstNonEmpty(tool.Description, "MCP tool "+tool.Name),
		Example: fmt.Sprintf(`{"name":"%s","arguments":{}}`, name), Parameters: schema, Metadata: metadata,
	}
	return core.DynamicNativeTool{
		Spec: spec,
		Handler: func(ctx context.Context, call *core.ToolCall) (string, error) {
			var args map[string]any
			if len(call.Arguments) > 0 {
				if err := json.Unmarshal(call.Arguments, &args); err != nil {
					return "", fmt.Errorf("parse MCP tool arguments: %w", err)
				}
			}
			req := approval.Request{Tool: spec.Name, Tier: "mcp_tool", Summary: "Call MCP tool", Details: "Server: " + serverName + "\nTool: " + tool.Name, Subject: mcpArgsSubject(args), Network: true}
			decision := core.Ask(req)
			if approvalMode(server) == constants.ApprovalPolicyAuto {
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
	keys := slices.Sorted(maps.Keys(args))
	return strings.Join(core.MapSlice(keys, func(k string) string {
		return fmt.Sprintf("%s=%v", k, args[k])
	}), " ")
}

func loadServerTools(ctx context.Context, name string, server config.MCPServerConfig) ([]mcpTool, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	client, err := persistentClient(ctx, name, server)
	if err != nil {
		return nil, err
	}
	var list toolList
	if err := client.request(ctx, "tools/list", map[string]any{}, &list); err != nil {
		evictClient(clientKey(name, server), client)
		return nil, err
	}
	return list.Tools, nil
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
		return string(raw), nil
	}
	return strings.Join(parts, "\n"), nil
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
	reader     *bufio.Reader
	stderr     *bytes.Buffer
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
	cmd := exec.Command(server.Command, server.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP server: %w", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	client := &stdioClient{cmd: cmd, stdin: stdin, reader: bufio.NewReader(stdout), stderr: &stderr, gate: make(chan struct{}, 1), done: done}
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
	if _, err := fmt.Fprintf(c.stdin, "%s\n", payload); err != nil {
		return fmt.Errorf("write MCP request: %w", err)
	}
	if strings.HasPrefix(method, "notifications/") {
		return nil
	}
	respBytes, err := c.readResponse(ctx, method, c.nextID)
	if err != nil {
		return err
	}
	return c.withStderr(decodeRPC(method, respBytes, out))
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

func (c *stdioClient) readResponse(ctx context.Context, method string, id int) ([]byte, error) {
	response := make(chan []byte, 1)
	readErr := make(chan error, 1)
	go func() {
		for {
			message, err := c.reader.ReadBytes('\n')
			if err != nil {
				readErr <- fmt.Errorf("read MCP message: %w", err)
				return
			}
			if !messageMatchesID(message, id) {
				// Notification or a response to a different in-flight request:
				// keep reading until the one we're waiting on arrives.
				continue
			}
			response <- message
			return
		}
	}()
	select {
	case respBytes := <-response:
		return respBytes, nil
	case err := <-readErr:
		return nil, c.withStderr(err)
	case <-ctx.Done():
		c.kill()
		return nil, fmt.Errorf("MCP %s: %w", method, ctx.Err())
	}
}

// messageMatchesID reports whether a raw JSON-RPC message carries the given
// response id. Notifications (no id field) decode to id 0, which never
// matches a real request id (ids start at 1), so they're naturally skipped.
// A message that fails to parse at all is treated as a match so the caller
// still surfaces the parse error instead of hanging forever.
func messageMatchesID(message []byte, id int) bool {
	var probe struct {
		ID int `json:"id"`
	}
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
	if err == nil || c == nil || c.stderr == nil || strings.TrimSpace(c.stderr.String()) == "" {
		return err
	}
	return fmt.Errorf("%w; stderr: %s", err, strings.TrimSpace(c.stderr.String()))
}

func (c *stdioClient) close() {
	if c == nil {
		return
	}
	c.kill()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
}

func (c *stdioClient) kill() {
	if c == nil {
		return
	}
	_ = c.stdin.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
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
	enabled := core.StringSet(server.EnabledTools)
	disabled := core.StringSet(server.DisabledTools)
	out := slices.DeleteFunc(slices.Clone(tools), func(tool mcpTool) bool {
		_, allowed := enabled[tool.Name]
		_, denied := disabled[tool.Name]
		return denied || (len(enabled) > 0 && !allowed)
	})
	slices.SortFunc(out, func(a, b mcpTool) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// approvalMode defaults to ask for every transport. HTTP servers are remote,
// third-party tool providers whose tool descriptions are model-visible
// untrusted text, so the default must never relax for them.
func approvalMode(server config.MCPServerConfig) string {
	if strings.TrimSpace(server.DefaultToolsApprovalMode) != "" {
		return server.DefaultToolsApprovalMode
	}
	return constants.ApprovalPolicyAsk
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
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() == 0 || b.String()[b.Len()-1] != '_' {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
