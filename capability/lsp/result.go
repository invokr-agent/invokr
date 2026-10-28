package lsp

import (
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/support/fsutil"
	"github.com/invokr-agent/invokr/tools/core"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// Location is one source span in the shape the model reads: a workspace path
// with 1-based line and column, matching how every other invokr tool prints
// positions. The wire form is 0-based, so the conversion happens here.
type Location struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"end_line,omitempty"`
	EndColumn int    `json:"end_column,omitempty"`
}

type Symbol struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Container string   `json:"container,omitempty"`
	Location  Location `json:"location"`
}

type Diagnostic struct {
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Source   string   `json:"source,omitempty"`
	Location Location `json:"location"`
}

// symbolKindNames indexes the 1-based LSP SymbolKind enum; slot 0 is unused.
var symbolKindNames = []string{"", "file", "module", "namespace", "package", "class", "method", "property", "field", "constructor", "enum", "interface", "function", "variable", "constant", "string", "number", "boolean", "array", "object", "key", "null", "enum_member", "struct", "event", "operator", "type_parameter"}

// severityNames indexes the 1-based LSP DiagnosticSeverity enum.
var severityNames = []string{"", "error", "warning", "information", "hint"}

func enumName(table []string, value int) string {
	if value <= 0 || value >= len(table) {
		return "unknown"
	}
	return table[value]
}

func zeroBased(n int) uint32 {
	if n <= 1 {
		return 0
	}
	return uint32(n - 1)
}

func oneBased(n uint32) int { return int(n) + 1 }

// rel reports a path relative to the workspace root when it is inside it, so
// results line up with what the other tools print, and an absolute path
// otherwise — definitions land in the module cache and the stdlib all the time.
func (c *Client) rel(docURI uri.URI) string {
	path := docURI.FsPath()
	if rel, ok := fsutil.RelInside(c.root, path); ok {
		return rel
	}
	return path
}

func (c *Client) location(in protocol.Location) Location {
	return Location{Path: c.rel(in.URI),
		Line: oneBased(in.Range.Start.Line), Column: c.column(in.URI, in.Range.Start),
		EndLine: oneBased(in.Range.End.Line), EndColumn: c.column(in.URI, in.Range.End)}
}

func (c *Client) locations(in []protocol.Location) []Location { return core.MapSlice(in, c.location) }

// definitionLocations flattens the three shapes the spec allows for a
// definition result into one list.
func (c *Client) definitionLocations(result protocol.DefinitionResult) []Location {
	switch value := result.(type) {
	case *protocol.Location:
		return c.locations([]protocol.Location{*value})
	case protocol.LocationSlice:
		return c.locations(value)
	case protocol.DefinitionLinkSlice:
		return core.MapSlice(value, func(link protocol.DefinitionLink) Location {
			return c.location(protocol.Location{URI: link.TargetURI, Range: link.TargetSelectionRange})
		})
	default:
		// Not nil: an empty definition result marshalled as `null` where an empty
		// references result marshalled as `[]`, for the same "found nothing".
		return []Location{}
	}
}

// documentSymbols and workspaceSymbols flatten the result shapes the spec
// allows for a symbol query into one list.
func (c *Client) documentSymbols(result protocol.DocumentSymbolResult, docURI uri.URI) []Symbol {
	if value, ok := result.(protocol.DocumentSymbolSlice); ok {
		return c.flatten(value, docURI, "")
	}
	return c.symbolInformationsOrEmpty(result)
}

func (c *Client) workspaceSymbols(result protocol.WorkspaceSymbolResult) []Symbol {
	if value, ok := result.(protocol.WorkspaceSymbolSlice); ok {
		return core.MapSlice(value, func(one protocol.WorkspaceSymbol) Symbol {
			return newSymbol(one.Name, int(one.Kind), one.ContainerName, c.workspaceSymbolLocation(one.Location))
		})
	}
	return c.symbolInformationsOrEmpty(result)
}

// symbolInformationsOrEmpty projects the legacy SymbolInformation shape, and an
// empty (never nil) list for anything else.
func (c *Client) symbolInformationsOrEmpty(result any) []Symbol {
	value, ok := result.(protocol.SymbolInformationSlice)
	if !ok {
		return []Symbol{}
	}
	return core.MapSlice(value, func(one protocol.SymbolInformation) Symbol {
		return newSymbol(one.Name, int(one.Kind), one.ContainerName, c.location(one.Location))
	})
}

// rankByMatch puts the symbols whose own name matches the query first.
// workspace/symbol is fuzzy — searching for RegisterLSPTool also returns a test
// named TestTokfResultFilterFallsBackToInputOnTokfFailure — and the model pays
// tokens reasoning about hits it should never have had to see.
func rankByMatch(found []Symbol, query string) []Symbol {
	want := strings.ToLower(query)
	score := func(one Symbol) int {
		name := strings.ToLower(lastSegment(one.Name))
		switch {
		case name == want:
			return 0
		case strings.HasPrefix(name, want):
			return 1
		case strings.Contains(name, want):
			return 2
		default:
			return 3
		}
	}
	slices.SortStableFunc(found, func(a, b Symbol) int { return score(a) - score(b) })
	return found
}

// lastSegment drops the qualification servers put in front of a name, so
// `(*Runner).Run` and `pkg.Thing` compare on `run` and `thing`.
func lastSegment(name string) string {
	if cut := strings.LastIndexAny(name, ".)"); cut >= 0 && cut+1 < len(name) {
		return name[cut+1:]
	}
	return name
}

// workspaceSymbolLocation handles the resolve-support arm of the spec, where a
// server may name only the file and defer the range to a resolve request the
// spike does not make.
func (c *Client) workspaceSymbolLocation(in protocol.WorkspaceSymbolLocation) Location {
	switch value := in.(type) {
	case *protocol.Location:
		return c.location(*value)
	case *protocol.LocationUriOnly:
		return Location{Path: c.rel(value.URI)}
	default:
		return Location{}
	}
}

// newSymbol builds the shared projection of an LSP symbol; callers supply the
// location, which differs per result shape.
func newSymbol(name string, kind int, container *string, loc Location) Symbol {
	sym := Symbol{Name: name, Kind: enumName(symbolKindNames, kind), Location: loc}
	if container != nil {
		sym.Container = *container
	}
	return sym
}

// flatten walks the hierarchical documentSymbol tree into a flat list, keeping
// the parent's name as the container so nesting survives the flattening.
func (c *Client) flatten(in []protocol.DocumentSymbol, docURI uri.URI, container string) []Symbol {
	out := make([]Symbol, 0, len(in))
	for _, one := range in {
		out = append(out, Symbol{Name: one.Name,
			Kind: enumName(symbolKindNames, int(one.Kind)), Container: container,
			Location: c.location(protocol.Location{URI: docURI, Range: one.SelectionRange})})
		out = append(out, c.flatten(one.Children, docURI, one.Name)...)
	}
	return out
}

func (c *Client) diagnostics(in []protocol.Diagnostic, docURI uri.URI) []Diagnostic {
	return core.MapSlice(in, func(one protocol.Diagnostic) Diagnostic {
		source, _ := one.Source.Get()
		return Diagnostic{Severity: enumName(severityNames, int(one.Severity)),
			Message: diagnosticMessage(one.Message), Source: source,
			Location: c.location(protocol.Location{URI: docURI, Range: one.Range})}
	})
}

// diagnosticMessage unwraps the string-or-markup union the spec allows.
func diagnosticMessage(in protocol.InlayHintTooltip) string {
	switch value := in.(type) {
	case protocol.String:
		return string(value)
	case *protocol.MarkupContent:
		return value.Value
	default:
		return ""
	}
}
