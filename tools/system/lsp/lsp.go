// Package lsp exposes symbol-accurate code navigation to the model as four
// read-path tools. It owns argument shape, workspace policy, and result
// framing; capability/lsp owns the protocol conversation.
//
// Four specs rather than one tool with an action enum, because `required` is
// where "definition needs a path, a line and a column" belongs: the provider
// enforces it before the call is made, which a shared description cannot. The
// spike's live test lost a turn to a model inventing coordinates that one
// merged schema had no way to reject. See docs/private/plans/lsp-spike-followup.md.
package lsp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	lspclient "github.com/invokr-ai/invokr/capability/lsp"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

// Namespace groups the four tools for tool_search and the metadata policy.
const Namespace = "lsp"

const (
	DefinitionTool  = "lsp_definition"
	ReferencesTool  = "lsp_references"
	SymbolsTool     = "lsp_symbols"
	DiagnosticsTool = "lsp_diagnostics"
)

// diagnosticsTimeout bounds the wait for the server's first verdict on a file.
// Servers index lazily, so the first call after startup is the slow one.
const diagnosticsTimeout = 15 * time.Second

// running holds the process-wide server pool so ShutdownServers can reach it
// from the runtime cleanup path, mirroring how the mcp tool tracks its clients.
var running struct {
	sync.Mutex
	manager *lspclient.Manager
	root    string
}

// Args is shared by all four tools. Which fields a given tool actually needs is
// expressed in its schema's `required`, not here.
type Args struct {
	Path   string `json:"path,omitempty" desc:"Workspace-relative file to ask about."`
	Line   int    `json:"line,omitempty" desc:"1-based line of the symbol."`
	Column int    `json:"column,omitempty" desc:"1-based column, pointing inside the symbol's name rather than at the start of the line."`
	Query  string `json:"query,omitempty" desc:"Symbol name to search for across the workspace."`
}

type locationsResponse struct {
	Locations []lspclient.Location `json:"locations"`
	Note      string               `json:"note,omitempty"`
}

// emptyLocationNote is what an empty list most often means. Without it a model
// reads "no locations" as "this symbol has none", which during the spike's live
// test turned a mistyped position into a confident "not defined in the
// workspace".
const emptyLocationNote = "no locations found; the position may not be on a symbol name — call " + SymbolsTool + " to get the exact line and column of the declaration"

type symbolsResponse struct {
	Symbols []lspclient.Symbol `json:"symbols"`
	Note    string             `json:"note,omitempty"`
}

// maxSymbols bounds what one call can put in the context window. A mid-sized
// file already returns a few KB of symbols, and a broad workspace query is
// unbounded.
const maxSymbols = 100

// capSymbols truncates to maxSymbols and says so, because a silently short list
// reads as a complete one.
func capSymbols(found []lspclient.Symbol) symbolsResponse {
	if len(found) <= maxSymbols {
		return symbolsResponse{Symbols: found}
	}
	return symbolsResponse{Symbols: found[:maxSymbols],
		Note: fmt.Sprintf("truncated to the first %d of %d symbols; narrow the query or ask about one file", maxSymbols, len(found))}
}

type diagnosticsResponse struct {
	Diagnostics []lspclient.Diagnostic `json:"diagnostics"`
}

// RegisterLSPTools registers the read-path tools only when a language server
// for the workspace is actually installed. The alternative — tools that are
// always offered and always fail — teaches the model to reach for something
// that does not work, which is the failure mode docs/private/plans/lsp-support.md
// calls out.
func RegisterLSPTools(registrar core.NativeRegistrar, deps core.CallerDeps, wsp workspace.Policy) {
	if !deps.App.LSP.IsEnabled() || wsp.Root == "" {
		return
	}
	manager := lspclient.NewManager(wsp.Root, Servers(deps.App.LSP))
	extensions := manager.Extensions()
	if len(extensions) == 0 {
		return
	}
	run := runner{manager: adopt(manager, wsp.Root), wsp: wsp}
	for _, tool := range readPathTools(run, extensions) {
		core.Register(registrar, tool)
	}
}

// readPathTools is the whole model-facing surface. The write path — rename and
// replace_symbol — is deliberately absent: capabilities are per-spec, so a
// write tool must be a fifth spec rather than a fifth action, or these four
// stop being read-only. See docs/private/plans/lsp-write-path.md.
func readPathTools(run runner, extensions []string) []core.Tool[Args] {
	kinds := strings.Join(extensions, ", ")
	// The wording is about which language servers are installed for this
	// workspace, not about a language: the set below is whatever happens to be
	// on PATH, and calling it "Go" made the model report a Go-only agent.
	files := "files the language servers installed for this workspace handle (" + kinds + ")"
	file := "file handled by an installed language server (" + kinds + ")"
	example := "src/example" + extensions[0]
	position := []string{"path", "line", "column"}
	return []core.Tool[Args]{
		tool(DefinitionTool,
			"Find where the symbol at a position is defined, in "+files+". Follows the language's own resolution rather than matching text, so it lands on the declaration even when the name is shared. Positions are 1-based; get them from "+SymbolsTool+" instead of guessing.",
			`{"name":"`+DefinitionTool+`","arguments":{"path":"`+example+`","line":42,"column":6}}`, position, position, run.definition),

		tool(ReferencesTool,
			"Find everything that refers to the symbol at a position, in "+files+", including the declaration. Unlike grep this finds interface implementations and skips same-named symbols in other scopes and comments. Positions are 1-based; get them from "+SymbolsTool+".",
			`{"name":"`+ReferencesTool+`","arguments":{"path":"`+example+`","line":42,"column":6}}`, position, position, run.references),

		tool(SymbolsTool,
			"List the declarations in one "+file+", or search the whole workspace by name. Pass path to list a file; pass query to search. This is how you turn a symbol name into the file, line and column the other lsp tools need.",
			`{"name":"`+SymbolsTool+`","arguments":{"query":"Runner"}}`, []string{"path", "query"}, nil, run.symbols),

		tool(DiagnosticsTool,
			"Report what the compiler currently thinks is wrong with one "+file+", without running a build. Re-reads the file first, so it reflects edits made this turn.",
			`{"name":"`+DiagnosticsTool+`","arguments":{"path":"`+example+`"}}`, []string{"path"}, []string{"path"}, run.diagnostics),
	}
}

func tool(name, description, example string, fields, required []string, execute func(context.Context, Args) (string, error)) core.Tool[Args] {
	return core.Tool[Args]{
		Spec: core.ToolSpec{
			Name: name, Description: description, Example: example,
			Parameters: core.SchemaFor[Args](core.WithOnly(fields...), core.WithRequired(required...)),
			// Read-only protocol operations still execute a host LSP server;
			// session policy handles the lsp namespace as unconfined execution.
			Metadata: core.ReadWorkspaceMetadata(Namespace, core.ToolCapabilities{ReadsWorkspace: true}),
		},
		Execute: execute,
	}
}

// runner carries what every tool needs: the server pool and the workspace
// boundary each path is checked against.
type runner struct {
	manager *lspclient.Manager
	wsp     workspace.Policy
}

// client resolves a path against the workspace policy and returns the server
// that owns that language.
func (r runner) client(ctx context.Context, path string) (*lspclient.Client, string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, "", fmt.Errorf("path is required; call %s with a query to turn a symbol name into a file and position", SymbolsTool)
	}
	resolved, err := r.wsp.Resolve(path)
	if err != nil {
		return nil, "", err
	}
	client, err := r.manager.For(ctx, resolved)
	if err != nil {
		return nil, "", err
	}
	return client, resolved, nil
}

// positionFunc is a method expression over Client, so definition and references
// share one body.
type positionFunc func(*lspclient.Client, context.Context, string, int, int) ([]lspclient.Location, error)

func (r runner) definition(ctx context.Context, args Args) (string, error) {
	return r.position(ctx, args, (*lspclient.Client).Definition)
}

func (r runner) references(ctx context.Context, args Args) (string, error) {
	return r.position(ctx, args, (*lspclient.Client).References)
}

func (r runner) position(ctx context.Context, args Args, query positionFunc) (string, error) {
	// Checked before the client is fetched: a bad position should not be worth
	// starting a language server for.
	if args.Line < 1 || args.Column < 1 {
		return "", fmt.Errorf("line and column are required and are 1-based; %s reports the exact position of a declaration", SymbolsTool)
	}
	client, path, err := r.client(ctx, args.Path)
	if err != nil {
		return "", err
	}
	found, err := query(client, ctx, path, args.Line, args.Column)
	if err != nil {
		return "", err
	}
	response := locationsResponse{Locations: found}
	if len(found) == 0 {
		response.Note = emptyLocationNote
	}
	return marshal(response)
}

// symbols is the one tool whose arguments are a genuine either/or, which no
// `required` list can express — so it validates at runtime and says which.
func (r runner) symbols(ctx context.Context, args Args) (string, error) {
	if strings.TrimSpace(args.Path) == "" {
		return r.searchSymbols(ctx, args)
	}
	client, path, err := r.client(ctx, args.Path)
	if err != nil {
		return "", err
	}
	return marshalSymbols(client.Symbols(ctx, path))
}

func (r runner) searchSymbols(ctx context.Context, args Args) (string, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("%s needs either a path, to list one file's declarations, or a query, to search the workspace", SymbolsTool)
	}
	client, err := r.manager.Any(ctx)
	if err != nil {
		return "", err
	}
	return marshalSymbols(client.SearchSymbols(ctx, query))
}

func (r runner) diagnostics(ctx context.Context, args Args) (string, error) {
	client, path, err := r.client(ctx, args.Path)
	if err != nil {
		return "", err
	}
	found, err := client.Diagnostics(ctx, path, diagnosticsTimeout)
	if err != nil {
		return "", err
	}
	return marshal(diagnosticsResponse{Diagnostics: found})
}

// adopt makes manager the process-wide one, reusing the existing manager when
// it already serves this root: a subagent loading the same tool set must not
// orphan the servers its parent already started.
func adopt(manager *lspclient.Manager, root string) *lspclient.Manager {
	running.Lock()
	previous := running.manager
	if previous != nil && running.root == root {
		running.Unlock()
		return previous
	}
	running.manager, running.root = manager, root
	running.Unlock()
	if previous != nil {
		previous.Close()
	}
	return manager
}

// ShutdownServers stops every language server started this session. Servers
// also self-terminate when the PID reported at initialize disappears, but that
// is a courtesy of the server, not a guarantee.
func ShutdownServers() {
	running.Lock()
	manager := running.manager
	running.manager = nil
	running.Unlock()
	if manager != nil {
		manager.Close()
	}
}

// Servers resolves the effective server table: the configured one when set,
// the built-in one otherwise. Exported so `invokr doctor` reports on exactly
// the table registration probes, rather than reimplementing the choice.
func Servers(cfg config.LSPConfig) []lspclient.ServerConfig {
	if len(cfg.Servers) == 0 {
		return lspclient.DefaultServers()
	}
	return core.MapSlice(cfg.Servers, func(server config.LSPServerConfig) lspclient.ServerConfig {
		return lspclient.ServerConfig{Command: server.Command, Args: server.Args, Extensions: server.Extensions, LanguageID: server.LanguageID}
	})
}

func marshalSymbols(found []lspclient.Symbol, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return marshal(capSymbols(found))
}

func marshal(v any) (string, error) {
	return core.MarshalResult(Namespace, v)
}
