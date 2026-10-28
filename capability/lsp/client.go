// Package lsp is a spike-grade Language Server Protocol client backing the
// `lsp` tool's symbol-accurate code navigation.
//
// Protocol types, JSON-RPC framing, and request dispatch all come from
// go.lsp.dev/protocol and go.lsp.dev/jsonrpc2; this package deliberately owns
// none of that — see docs/private/plans/lsp-support.md for why in-tree protocol code
// was rejected. What is left is the part no library supplies: server process
// lifecycle, the "open the document before asking about it" handshake, and a
// result shape small enough to put in front of a model.
package lsp

import (
	"bufio"
	"context"
	"fmt"
	"github.com/invokr-agent/invokr/support/strutil"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

const shutdownTimeout = 2 * time.Second

// handshakeTimeout bounds initialize. A binary that starts but never speaks LSP
// — a misconfigured lsp.servers entry — would otherwise block forever, and
// Manager.client holds its lock across Start, wedging every later call too.
// A var so tests can shorten it rather than wait it out.
var handshakeTimeout = 20 * time.Second

// ServerConfig describes one language server subprocess and the files it owns.
type ServerConfig struct {
	Command          string
	Args, Extensions []string
	LanguageID       string
}

// Client is an initialized connection to one language server.
type Client struct {
	server  protocol.Server
	conn    jsonrpc2.Conn
	root    string
	langID  protocol.LanguageKind
	handler *clientHandler
	kill    func()

	// encoding is what the server picked at initialize; every position crossing
	// this boundary is converted against it. See encoding.go.
	encoding protocol.PositionEncodingKind

	// syncGate serializes sync: the check, the notification and the record it
	// leaves have to be one step, or two calls naming the same file open it
	// twice. Separate from mu, which guards the maps below and must not be held
	// across a write to the server.
	//
	// A one-token channel rather than a Mutex because acquiring it has to be
	// cancellable. sync holds it across a write to the server's stdin, and a
	// server that has stopped reading blocks that write for as long as it likes
	// — on a Mutex every later call would then be stuck uninterruptibly, past
	// the point where the turn was abandoned or the user asked to stop. Same
	// gate stdioClient.acquire uses in tools/system/mcp for the same reason.
	syncGate chan struct{}

	mu     sync.Mutex
	opened map[uri.URI]openedDoc
	files  map[string]cachedFile
	// tick orders cache use, so a full cache evicts its oldest entry.
	tick uint64
}

// openedDoc is what the server was last told about a document. stamp is how a
// later call notices the file changed underneath it; version is the didChange
// counter the protocol requires to be monotonic per document. text is that same
// copy, kept because a wire position has to be converted against the text the
// server was reading, not against whatever is on disk by the time we ask.
type openedDoc struct {
	stamp   string
	version int32
	sourceText
}

// Start launches cfg.Command rooted at root and completes the LSP handshake.
// ctx bounds the handshake only; the connection lives until Close.
func Start(ctx context.Context, cfg ServerConfig, root string) (*Client, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = root
	// Not io.Discard: when a server misbehaves — refusing the handshake, dying
	// mid-session — its stderr is the only account of why.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe for %s: %w", cfg.Command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe for %s: %w", cfg.Command, err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe for %s: %w", cfg.Command, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
	}
	go logStderr(cfg.Command, stderr)
	kill := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return Connect(ctx, jsonrpc2.NewStream(stdio{stdout, stdin}), cfg, root, kill)
}

// logStderr drains a server's stderr into the debug log. Draining is the part
// that is not optional: a full pipe blocks the server on write, and a server
// blocked on stderr answers no more requests.
//
// So the scan ending is not the end of the job. bufio.Scanner gives up on a
// line longer than its buffer — a rust-analyzer trace line or a gopls panic
// backtrace reaches that — and stopping there would wedge the very server this
// goroutine exists to explain. Past that point the log is forfeit but the
// draining continues.
func logStderr(command string, stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		zap.L().Debug("language server stderr", zap.String("server", command), zap.String("line", scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		zap.L().Debug("language server stderr no longer logged", zap.String("server", command), zap.Error(err))
		_, _ = io.Copy(io.Discard, stderr)
	}
}

// Connect drives the handshake over an already-open stream. Start uses it for
// a subprocess; tests use it against an in-memory server pair, which is the
// only reason the spike needs no language server installed to be tested.
func Connect(ctx context.Context, stream jsonrpc2.Stream, cfg ServerConfig, root string, kill func()) (*Client, error) {
	handler := newClientHandler()
	// The connection's read loop outlives this call, so it must not inherit a
	// request-scoped cancel.
	_, conn, server := protocol.NewClient(context.WithoutCancel(ctx), handler, stream)
	client := &Client{
		server:   server,
		conn:     conn,
		root:     root,
		langID:   protocol.LanguageKind(cfg.LanguageID),
		handler:  handler,
		kill:     kill,
		opened:   map[uri.URI]openedDoc{},
		files:    map[string]cachedFile{},
		syncGate: make(chan struct{}, 1),
	}
	// Prefilled: the token in the channel is the right to sync.
	client.syncGate <- struct{}{}
	handshake, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := client.initialize(handshake); err != nil {
		// A server that never answered initialize will not answer shutdown
		// either, so drop the connection rather than paying shutdownTimeout.
		_ = client.abandon()
		return nil, err
	}
	return client, nil
}

func (c *Client) initialize(ctx context.Context) error {
	root := uri.File(c.root)
	pid := int32(os.Getpid())
	params := &protocol.InitializeParams{ProcessID: &pid}
	params.Capabilities.General = &protocol.GeneralClientCapabilities{PositionEncodings: offeredEncodings()}
	// Reporting our PID is what lets a well-behaved server reap itself if
	// invokr dies without a clean shutdown.
	params.WorkspaceFolders = protocol.NewNullable([]protocol.WorkspaceFolder{
		{URI: root, Name: filepath.Base(c.root)},
	})
	result, err := c.server.Initialize(ctx, params)
	if err != nil {
		return fmt.Errorf("lsp initialize: %w", err)
	}
	c.encoding = negotiatedEncoding(result)
	if err := c.server.Initialized(ctx, &protocol.InitializedParams{}); err != nil {
		return fmt.Errorf("lsp initialized: %w", err)
	}
	return nil
}

// Close asks the server to shut down, then makes sure the process is gone.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = c.server.Shutdown(ctx)
	_ = c.server.Exit(ctx)
	return c.abandon()
}

// abandon drops the connection and the process without the shutdown handshake.
func (c *Client) abandon() error {
	err := c.conn.Close()
	if c.kill != nil {
		c.kill()
	}
	return err
}

// Definition answers "where is the symbol at this position defined".
func (c *Client) Definition(ctx context.Context, path string, line, column int) ([]Location, error) {
	at, err := c.at(ctx, path, line, column)
	if err != nil {
		return nil, err
	}
	found, err := c.server.Definition(ctx, &protocol.DefinitionParams{TextDocumentPositionParams: at})
	if err != nil {
		return nil, fmt.Errorf("textDocument/definition: %w", err)
	}
	return c.definitionLocations(found), nil
}

// References answers "what refers to the symbol at this position", including
// the declaration itself so the model sees the whole set in one call.
func (c *Client) References(ctx context.Context, path string, line, column int) ([]Location, error) {
	at, err := c.at(ctx, path, line, column)
	if err != nil {
		return nil, err
	}
	params := &protocol.ReferenceParams{
		TextDocumentPositionParams: at,
		Context:                    protocol.ReferenceContext{IncludeDeclaration: true},
	}
	found, err := c.server.References(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("textDocument/references: %w", err)
	}
	return c.locations(found), nil
}

func (c *Client) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	docURI, err := c.sync(ctx, path)
	if err != nil {
		return nil, err
	}
	params := &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: docURI}}
	found, err := c.server.DocumentSymbol(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("textDocument/documentSymbol: %w", err)
	}
	return c.documentSymbols(found, docURI), nil
}

// SearchSymbols looks a symbol name up across the whole workspace, which is
// how the model gets the file and position the other actions need.
func (c *Client) SearchSymbols(ctx context.Context, query string) ([]Symbol, error) {
	found, err := c.server.Symbols(ctx, &protocol.WorkspaceSymbolParams{Query: query})
	if err != nil {
		return nil, fmt.Errorf("workspace/symbol: %w", err)
	}
	return rankByMatch(c.workspaceSymbols(found), query), nil
}

// Diagnostics returns the server's current verdict on one file — a
// compiler-accurate error list without running a build. It waits up to
// timeout for the first publishDiagnostics naming the document.
func (c *Client) Diagnostics(ctx context.Context, path string, timeout time.Duration) ([]Diagnostic, error) {
	docURI, err := c.sync(ctx, path)
	if err != nil {
		return nil, err
	}
	published, ok := c.handler.wait(ctx, docURI, timeout)
	if !ok {
		return nil, fmt.Errorf("no diagnostics published for %s within %s", path, timeout)
	}
	return c.diagnostics(published, docURI), nil
}

// sync tells the server about a document before it is asked about: didOpen on
// first use, didChange whenever the file has moved on since.
//
// The re-sync is what keeps a read honest across a turn. Agents edit files
// mid-turn, and a server whose copy is the pre-edit one answers confidently
// with the pre-edit truth — diagnostics on a file broken by apply_patch came
// back clean, which reads as "no problems" rather than "stale".
func (c *Client) sync(ctx context.Context, path string) (uri.URI, error) {
	docURI := uri.File(path)
	// Held for the rest of the function, and taken before the file is read.
	// Two tools naming one file in the same turn — lsp_diagnostics and
	// lsp_definition, which the four-tool split makes the normal pattern —
	// would otherwise both read "not opened" and both send didOpen at version 1,
	// which gopls rejects as a duplicate. Reading inside the gate also stops the
	// loser of a race from sending a didChange carrying the older text.
	if err := c.acquireSync(ctx); err != nil {
		return docURI, err
	}
	defer func() { c.syncGate <- struct{}{} }()
	text, stamp, err := readStamped(path)
	if err != nil {
		return docURI, err
	}
	c.mu.Lock()
	known, opened := c.opened[docURI]
	c.mu.Unlock()
	if opened && known.stamp == stamp {
		return docURI, nil
	}
	next := openedDoc{stamp: stamp, version: known.version + 1, sourceText: newSourceText(text)}
	if err := c.notify(ctx, docURI, text, opened, next.version); err != nil {
		return docURI, err
	}
	c.mu.Lock()
	c.opened[docURI] = next
	c.mu.Unlock()
	// The server's previous verdict described the previous text.
	if opened {
		c.handler.forget(docURI)
	}
	return docURI, nil
}

// acquireSync takes the sync gate, or gives up when ctx is done rather than
// waiting on a server that may never let go.
func (c *Client) acquireSync(ctx context.Context) error {
	select {
	case <-c.syncGate:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("lsp sync %s", context.Cause(ctx))
	}
}

func (c *Client) notify(ctx context.Context, docURI uri.URI, text string, opened bool, version int32) error {
	if opened {
		params := &protocol.DidChangeTextDocumentParams{
			TextDocument: protocol.VersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: docURI},
				Version:                version,
			},
			// Whole-document replacement: cheap, and it cannot drift out of step
			// with the file the way an incremental range would.
			ContentChanges: []protocol.TextDocumentContentChangeEvent{
				&protocol.TextDocumentContentChangeWholeDocument{Text: text},
			},
		}
		if err := c.server.DidChange(ctx, params); err != nil {
			return fmt.Errorf("lsp didChange %s: %w", docURI.FsPath(), err)
		}
		return nil
	}
	item := protocol.TextDocumentItem{URI: docURI, LanguageID: c.langID, Version: version, Text: text}
	if err := c.server.DidOpen(ctx, &protocol.DidOpenTextDocumentParams{TextDocument: item}); err != nil {
		return fmt.Errorf("lsp didOpen %s: %w", docURI.FsPath(), err)
	}
	return nil
}

// readStamped returns a file's text with an identity for it. The text has to be
// read anyway to send it, so the stamp hashes the content rather than trusting
// size and mtime, which a same-length edit inside one timestamp tick can fool.
func readStamped(path string) (string, string, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(text), strutil.HashHex(string(text)), nil
}

func (c *Client) at(ctx context.Context, path string, line, column int) (protocol.TextDocumentPositionParams, error) {
	docURI, err := c.sync(ctx, path)
	if err != nil {
		return protocol.TextDocumentPositionParams{}, err
	}
	return protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: docURI},
		Position:     c.position(docURI, line, column),
	}, nil
}

// stdio adapts a subprocess stdout/stdin pair to the io.ReadWriteCloser the
// jsonrpc2 header stream expects.
type stdio struct {
	io.ReadCloser
	io.WriteCloser
}

func (s stdio) Close() error {
	_ = s.WriteCloser.Close()
	return s.ReadCloser.Close()
}
