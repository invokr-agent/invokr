package lsp

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// DefaultServers is the table of language servers the spike knows how to
// launch. Nothing here is installed by invokr; an entry is inert until the
// command is actually on PATH.
func DefaultServers() []ServerConfig {
	return []ServerConfig{
		{Command: "gopls", Extensions: []string{".go"}, LanguageID: "go"},
		{Command: "rust-analyzer", Extensions: []string{".rs"}, LanguageID: "rust"},
		{Command: "pyright-langserver", Args: []string{"--stdio"}, Extensions: []string{".py"}, LanguageID: "python"},
		{Command: "typescript-language-server", Args: []string{"--stdio"}, Extensions: []string{".ts", ".tsx"}, LanguageID: "typescript"},
	}
}

// Manager owns one server process per language, started on first use and kept
// alive for the rest of the session: the handshake and the server's initial
// index are far too expensive to pay per tool call.
type Manager struct {
	root    string
	servers []ServerConfig

	probe     sync.Once
	installed []ServerConfig

	mu      sync.Mutex
	clients map[string]*Client
}

func NewManager(root string, servers []ServerConfig) *Manager {
	return &Manager{root: root, servers: servers, clients: map[string]*Client{}}
}

// Installed lists the configured servers whose command is on PATH. The `lsp`
// tool is registered only when this is non-empty — a tool that can only ever
// return "no language server" is worse than a missing one.
//
// Probed once and remembered: every For, Any, and Extensions call asks, and
// PATH does not change under a running process in any way that matters here.
func (m *Manager) Installed() []ServerConfig {
	m.probe.Do(func() {
		for _, server := range m.servers {
			if _, err := exec.LookPath(server.Command); err == nil {
				m.installed = append(m.installed, server)
			}
		}
	})
	return m.installed
}

// Extensions lists the file extensions the installed servers cover, so the
// tool description can tell the model which files it can ask about.
func (m *Manager) Extensions() []string {
	out := make([]string, 0, len(m.servers))
	for _, server := range m.Installed() {
		for _, ext := range server.Extensions {
			if !slices.Contains(out, ext) {
				out = append(out, ext)
			}
		}
	}
	return out
}

// For returns a started client for path's language, launching the server on
// first use.
func (m *Manager) For(ctx context.Context, path string) (*Client, error) {
	ext := strings.ToLower(filepath.Ext(path))
	for _, server := range m.Installed() {
		if slices.Contains(server.Extensions, ext) {
			return m.client(ctx, server)
		}
	}
	return nil, m.missingServerError(ext)
}

// missingServerError names the extension asked for and the server that would
// have to be installed to serve it, so the model reads a missing tool rather
// than an unsupported language.
func (m *Manager) missingServerError(ext string) error {
	if ext == "" {
		ext = "(no extension)"
	}
	for _, server := range m.servers {
		if slices.Contains(server.Extensions, ext) {
			return fmt.Errorf("no language server running for %s files: %s is not installed on PATH; active extensions: %s",
				ext, server.Command, m.extensionList())
		}
	}
	return fmt.Errorf("no language server configured for %s files; active extensions: %s", ext, m.extensionList())
}

func (m *Manager) extensionList() string {
	extensions := m.Extensions()
	if len(extensions) == 0 {
		return "none"
	}
	return strings.Join(extensions, ", ")
}

// Any returns a client for the first installed server, for workspace-wide
// queries that name no file.
func (m *Manager) Any(ctx context.Context) (*Client, error) {
	installed := m.Installed()
	if len(installed) == 0 {
		return nil, fmt.Errorf("no language server available")
	}
	return m.client(ctx, installed[0])
}

func (m *Manager) client(ctx context.Context, server ServerConfig) (*Client, error) {
	// Held across Start so two concurrent tool calls cannot race two servers
	// into existence for the same language.
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.clients[server.Command]; ok {
		return existing, nil
	}
	started, err := Start(ctx, server, m.root)
	if err != nil {
		return nil, err
	}
	m.clients[server.Command] = started
	return started, nil
}

// Close shuts down every running server.
func (m *Manager) Close() {
	m.mu.Lock()
	running := m.clients
	m.clients = map[string]*Client{}
	m.mu.Unlock()
	for _, client := range running {
		_ = client.Close()
	}
}
