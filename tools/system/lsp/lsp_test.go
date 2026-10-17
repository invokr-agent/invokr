package lsp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	lspclient "github.com/invokr-ai/invokr/capability/lsp"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

type fakeRegistrar struct {
	specs []core.ToolSpec
}

func (r *fakeRegistrar) RegisterNative(spec core.ToolSpec, _ core.NativeHandler) {
	r.specs = append(r.specs, spec)
}

func (r *fakeRegistrar) HasNative(string) bool { return false }

func (r *fakeRegistrar) NativeSource(string) string { return "" }

func (r *fakeRegistrar) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	return nil
}

func (r *fakeRegistrar) names() []string {
	out := make([]string, 0, len(r.specs))
	for _, spec := range r.specs {
		out = append(out, spec.Name)
	}
	return out
}

func (r *fakeRegistrar) spec(t *testing.T, name string) core.ToolSpec {
	t.Helper()
	for _, spec := range r.specs {
		if spec.Name == name {
			return spec
		}
	}
	t.Fatalf("tool %q was not registered; got %v", name, r.names())
	return core.ToolSpec{}
}

// installFakeServer puts an executable of the given name on PATH so the
// conditional-registration probe finds a server without one being installed.
func installFakeServer(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, name)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write fake server: %v", err)
	}
	t.Setenv("PATH", dir)
}

func register(t *testing.T, cfg config.LSPConfig, root string) *fakeRegistrar {
	t.Helper()
	t.Cleanup(ShutdownServers)
	registrar := &fakeRegistrar{}
	deps := core.CallerDeps{App: config.AppConfig{LSP: cfg, Workspace: config.WorkspaceConfig{Root: root}}}
	RegisterLSPTools(registrar, deps, workspace.NewWorkspacePolicy(deps.App.Workspace))
	return registrar
}

func goServer() config.LSPConfig {
	return config.LSPConfig{Servers: []config.LSPServerConfig{
		{Command: "fake-gopls", Extensions: []string{".go"}, LanguageID: "go"},
	}}
}

func testRunner(t *testing.T, root string) runner {
	t.Helper()
	manager := lspclient.NewManager(root, Servers(goServer()))
	t.Cleanup(manager.Close)
	return runner{
		manager: manager,
		wsp:     workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}),
	}
}

func required(t *testing.T, spec core.ToolSpec) []string {
	t.Helper()
	value, ok := spec.Parameters["required"]
	if !ok {
		return nil
	}
	list, ok := value.([]string)
	if !ok {
		t.Fatalf("tool %q has a non-[]string required: %#v", spec.Name, value)
	}
	return list
}

func properties(t *testing.T, spec core.ToolSpec) map[string]any {
	t.Helper()
	value, ok := spec.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool %q has no properties map: %#v", spec.Name, spec.Parameters)
	}
	return value
}

func TestAllFourReadPathToolsAreRegistered(t *testing.T) {
	installFakeServer(t, "fake-gopls")

	registrar := register(t, goServer(), t.TempDir())

	want := []string{DefinitionTool, ReferencesTool, SymbolsTool, DiagnosticsTool}
	got := registrar.names()
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// The whole point of the split: the provider rejects a call with no position
// before it is ever made, which one merged schema could not express.
func TestPositionToolsRequirePathLineAndColumn(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	registrar := register(t, goServer(), t.TempDir())

	for _, name := range []string{DefinitionTool, ReferencesTool} {
		spec := registrar.spec(t, name)
		want := []string{"path", "line", "column"}
		if got := required(t, spec); !slices.Equal(got, want) {
			t.Errorf("%s: expected required %v, got %v", name, want, got)
		}
		for _, field := range want {
			if _, ok := properties(t, spec)[field]; !ok {
				t.Errorf("%s: expected a %q property", name, field)
			}
		}
		if _, ok := properties(t, spec)["query"]; ok {
			t.Errorf("%s: should not accept a query", name)
		}
	}
}

func TestDiagnosticsRequiresOnlyAPath(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	spec := register(t, goServer(), t.TempDir()).spec(t, DiagnosticsTool)

	if got := required(t, spec); !slices.Equal(got, []string{"path"}) {
		t.Fatalf("expected required [path], got %v", got)
	}
	if len(properties(t, spec)) != 1 {
		t.Fatalf("expected only a path property, got %#v", properties(t, spec))
	}
}

// symbols is the one genuine either/or, which `required` cannot express — so it
// must require nothing and validate at runtime.
func TestSymbolsRequiresNothingAndTakesPathOrQuery(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	spec := register(t, goServer(), t.TempDir()).spec(t, SymbolsTool)

	if got := required(t, spec); len(got) != 0 {
		t.Fatalf("expected no required fields, got %v", got)
	}
	for _, field := range []string{"path", "query"} {
		if _, ok := properties(t, spec)[field]; !ok {
			t.Fatalf("expected a %q property, got %#v", field, properties(t, spec))
		}
	}
}

// Load-bearing: CommandService.ReadOnlyTools keys off exactly these flags, so a
// write capability here would silently drop the whole read path out of it. This
// is the guard the write path must not quietly break.
func TestEveryToolStaysReadOnly(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	registrar := register(t, goServer(), t.TempDir())

	for _, spec := range registrar.specs {
		capabilities := spec.Metadata.Capabilities
		if !capabilities.ReadsWorkspace {
			t.Errorf("%s: expected ReadsWorkspace", spec.Name)
		}
		if capabilities.WritesWorkspace || capabilities.Network || capabilities.ExecutesCode {
			t.Errorf("%s: expected a read-only tool, got %#v", spec.Name, capabilities)
		}
		if spec.Metadata.Namespace != Namespace {
			t.Errorf("%s: expected namespace %q, got %q", spec.Name, Namespace, spec.Metadata.Namespace)
		}
	}
}

func TestDescriptionsNameTheCoveredExtensions(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	registrar := register(t, goServer(), t.TempDir())

	for _, spec := range registrar.specs {
		if !strings.Contains(spec.Description, ".go") {
			t.Errorf("%s: expected the description to name the covered extensions, got %q", spec.Name, spec.Description)
		}
		if !strings.Contains(spec.Example, spec.Name) {
			t.Errorf("%s: expected the example to call the tool itself, got %q", spec.Name, spec.Example)
		}
	}
}

// Each spec must own its schema: a shared inner map would let one tool's
// description leak into another's.
func TestSchemasAreNotShared(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	registrar := register(t, goServer(), t.TempDir())

	definition := properties(t, registrar.spec(t, DefinitionTool))["path"].(map[string]any)
	references := properties(t, registrar.spec(t, ReferencesTool))["path"].(map[string]any)
	definition["description"] = "mutated"
	if references["description"] == "mutated" {
		t.Fatal("expected each tool's schema to be independent")
	}
}

// Registering a tool that can only ever fail teaches the model to reach for
// something that does not work, so absence of a server means absence of tools.
func TestNothingIsRegisteredWithoutAnInstalledServer(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if registrar := register(t, goServer(), t.TempDir()); len(registrar.specs) != 0 {
		t.Fatalf("expected no registration, got %v", registrar.names())
	}
}

func TestNothingIsRegisteredWhenDisabled(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	disabled := goServer()
	off := false
	disabled.Enabled = &off

	if registrar := register(t, disabled, t.TempDir()); len(registrar.specs) != 0 {
		t.Fatalf("expected no registration when lsp is disabled, got %v", registrar.names())
	}
}

func TestNothingIsRegisteredWithoutAWorkspaceRoot(t *testing.T) {
	installFakeServer(t, "fake-gopls")

	if registrar := register(t, goServer(), ""); len(registrar.specs) != 0 {
		t.Fatalf("expected no registration without a workspace root, got %v", registrar.names())
	}
}

func TestConfiguredServersReplaceTheBuiltInTable(t *testing.T) {
	custom := Servers(config.LSPConfig{Servers: []config.LSPServerConfig{
		{Command: "my-ls", Args: []string{"--stdio"}, Extensions: []string{".zig"}, LanguageID: "zig"},
	}})
	if len(custom) != 1 || custom[0].Command != "my-ls" || custom[0].LanguageID != "zig" {
		t.Fatalf("expected the configured server, got %#v", custom)
	}

	if len(Servers(config.LSPConfig{})) == 0 {
		t.Fatal("expected the built-in table when no servers are configured")
	}
}

// The live spike showed a model with a symbol name but no position guessing
// coordinates left over from an earlier answer. Every error on that road must
// name the way out.
func TestMissingArgumentErrorsPointAtTheSymbolTool(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	run := testRunner(t, t.TempDir())

	cases := map[string]func() (string, error){
		"definition without a position": func() (string, error) {
			return run.definition(t.Context(), Args{Path: "widget.go"})
		},
		"references without a column": func() (string, error) {
			return run.references(t.Context(), Args{Path: "widget.go", Line: 4})
		},
		"definition without a path": func() (string, error) {
			return run.definition(t.Context(), Args{Line: 4, Column: 2})
		},
		"symbols with neither path nor query": func() (string, error) {
			return run.symbols(t.Context(), Args{})
		},
		"diagnostics without a path": func() (string, error) {
			return run.diagnostics(t.Context(), Args{})
		},
	}
	for name, call := range cases {
		_, err := call()
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if !strings.Contains(err.Error(), SymbolsTool) {
			t.Errorf("%s: expected the error to name %s, got %q", name, SymbolsTool, err)
		}
	}
}

// A position query must be rejected before a language server is started for it.
func TestBadPositionIsRejectedBeforeStartingAServer(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no server anywhere
	run := testRunner(t, t.TempDir())

	if _, err := run.definition(t.Context(), Args{Path: "widget.go", Line: 0, Column: 0}); err == nil {
		t.Fatal("expected an error for a missing position")
	} else if strings.Contains(err.Error(), "no language server") {
		t.Fatalf("expected the position to be checked first, got %q", err)
	}
}

func TestPathsOutsideTheWorkspaceAreRefused(t *testing.T) {
	installFakeServer(t, "fake-gopls")
	run := testRunner(t, t.TempDir())

	_, err := run.diagnostics(t.Context(), Args{Path: filepath.Join(t.TempDir(), "elsewhere.go")})
	if err == nil {
		t.Fatal("expected a path outside the workspace to be refused")
	}
	if !strings.Contains(err.Error(), "outside allowed workspace roots") {
		t.Fatalf("expected a workspace-policy error, got %q", err)
	}
}

func TestSymbolListIsCappedWithANote(t *testing.T) {
	many := make([]lspclient.Symbol, maxSymbols+10)
	capped := capSymbols(many)

	if len(capped.Symbols) != maxSymbols {
		t.Fatalf("expected %d symbols, got %d", maxSymbols, len(capped.Symbols))
	}
	if capped.Note == "" {
		t.Fatal("expected a truncation note; a silently short list reads as a complete one")
	}
	if short := capSymbols(many[:3]); short.Note != "" {
		t.Fatalf("expected no note for a short list, got %q", short.Note)
	}
}
