package agentui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/surface/ui"
	toolcore "github.com/invokr-ai/invokr/tools/core"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// toolPreviewMax bounds the one-line primary-arg preview. Single knob for width.
const toolPreviewMax = 48

// verbWidth keeps the verb column fixed so the preview/duration columns don't jitter.
const verbWidth = 9

// Nerd Font glyphs (Font Awesome / Devicon codepoints). Single-width in a Nerd Font.
const (
	iconDefault = "" // bolt
	iconMCP     = "" // plug
	glyphRun    = "▸" // ▸ (plain renderer; the TUI uses a spinner)
	glyphOK     = "✓" // ✓
	glyphFail   = "✕" // ✕
	glyphStop   = "⏹" // ⏹ (a call the user cancelled)
)

type toolGlyph struct {
	icon string
	verb string
}

// toolGlyphs maps a tool name to its icon + short verb. Unknown tools fall back
// to iconDefault + the raw name. mcp_* tools are matched by prefix in glyphFor.
var toolGlyphs = map[string]toolGlyph{
	"shell":            {"", "shell"},    // terminal
	"file_search":      {"", "search"},   // search
	"web_search":       {"", "web"},      // globe
	"apply_patch":      {"", "patch"},    // diff
	"memory":           {"", "memory"},   // brain
	"recall":           {"", "recall"},   // brain
	"code_interpreter": {"", "code"},     // python
	"retrieval":        {"", "retrieve"}, // database
	"delegate_task":    {"", "delegate"}, // users
	"load_skill":       {"", "skill"},    // graduation cap
	"session_search":   {"", "sessions"}, // history
	"tool_search":      {"", "tools"},    // search-plus
}

func glyphFor(name string) toolGlyph {
	if g, ok := toolGlyphs[name]; ok {
		return g
	}
	if strings.HasPrefix(name, "mcp_") {
		return toolGlyph{iconMCP, "mcp"}
	}
	return toolGlyph{iconDefault, cmp.Or(name, "tool")}
}

// primaryArgKeys lists, per tool, the argument keys to prefer for the preview.
var primaryArgKeys = map[string][]string{
	"shell": {"command"}, "file_search": {"query", "pattern", "path"}, "web_search": {"query"}, "memory": {"query", "content", "operation"},
	"recall": {"query"}, "code_interpreter": {"code"}, "retrieval": {"query"}, "session_search": {"query"},
	"delegate_task": {"task", "prompt"}, "load_skill": {"name", "skill"}, "create_tool": {"name"}, "tool_search": {"query"},
}

// primaryArg extracts a short, scannable preview of the call's main argument.
func primaryArg(name, argsJSON string) string {
	argsJSON = strings.TrimSpace(argsJSON)
	if argsJSON == "" {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &fields); err != nil {
		return truncatePreview(strings.Join(strings.Fields(argsJSON), " "))
	}

	val := ""
	for _, key := range primaryArgKeys[name] {
		if v, ok := fields[key]; ok {
			val = stringifyArg(v)
			break
		}
	}
	val = cmp.Or(val, firstStringField(fields))
	// shell: append the args list after the command.
	if rest, ok := fields["args"].([]any); ok && name == "shell" && len(rest) > 0 {
		parts := toolcore.MapSlice(rest, stringifyArg)
		val = strings.TrimSpace(val + " " + strings.Join(parts, " "))
	}
	return truncatePreview(strings.Join(strings.Fields(val), " "))
}

func stringifyArg(v any) string {
	switch v.(type) {
	case string, float64, bool:
		return fmt.Sprint(v)
	}
	return ""
}

// firstStringField picks a fallback preview value. Keys are sorted because Go
// randomises map iteration: without it the chip flickers between fields on
// every re-render of an already-finished row.
func firstStringField(fields map[string]any) string {
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		if s, ok := fields[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// truncatePreview keeps the tail for path-like values, the head otherwise.
func truncatePreview(s string) string {
	r := []rune(s)
	if len(r) <= toolPreviewMax {
		return s
	}
	if strings.Contains(s, "/") {
		return "..." + string(r[len(r)-(toolPreviewMax-3):])
	}
	return string(r[:toolPreviewMax-3]) + "..."
}

// renderToolLine formats a single tool line for the plain (streaming) renderer,
// which has no pane to align against. lead is prepended (a glyph, or the
// spinner frame in the TUI).
func renderToolLine(lead, name, preview, trailing string, state toolStatus) string {
	return renderToolLineWidth(lead, name, preview, trailing, state, 0)
}

// renderToolLineWidth lays out the design's tool row: an outcome-colored status
// glyph, a fixed verb column, the argument on a filled chip, and the metadata
// right-aligned to the pane's edge. Before the redesign every part of the line
// shared one Muted (or Danger) style and the metadata trailed inline.
func renderToolLineWidth(lead, name, preview, trailing string, state toolStatus, width int) string {
	g := glyphFor(name)
	head := statusStyle(state).Render(lead) + " " + ui.Invokr.Muted.Render(g.icon) + " " + ui.Invokr.Text.Render(fmt.Sprintf("%-*s", verbWidth, g.verb))
	meta := metaStyle(state).Render(trailing)
	if width <= 0 {
		return strings.TrimRight(head+" "+preview+"  "+trailing, " ")
	}
	// The chip's own padding costs two columns on top of the argument text.
	room := width - ansi.StringWidth(head) - ansi.StringWidth(trailing) - 4
	chip := ""
	if preview != "" && room > 1 {
		chip = ui.Invokr.Chip(ansi.Truncate(preview, room, "…"))
	}
	gap := max(1, width-ansi.StringWidth(head)-1-ansi.StringWidth(chip)-ansi.StringWidth(trailing))
	line := head + " " + chip + strings.Repeat(" ", gap) + meta
	if ansi.StringWidth(line) > width {
		// Nothing left to give: the verb column alone overflows the pane.
		return ansi.Truncate(line, max(1, width-1), "") + "…"
	}
	return line
}

// statusStyle colors the leading glyph by outcome — the design's one signal that
// scanning a column of tool calls is supposed to read from.
func statusStyle(state toolStatus) lipgloss.Style {
	switch state {
	case toolDone:
		return ui.Invokr.Success
	case toolFailed:
		return ui.Invokr.Danger
	default:
		return ui.Invokr.Info
	}
}

// metaStyle keeps the trailing metadata muted unless the call failed, where the
// "failed" word carries the outcome.
func metaStyle(state toolStatus) lipgloss.Style {
	if state == toolFailed {
		return ui.Invokr.Danger
	}
	return ui.Invokr.Muted
}
