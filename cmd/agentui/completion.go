package agentui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/antonikliment/tuikit"
	"github.com/invokr-ai/invokr/surface/ui"
	toolcore "github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/file_search"

	tea "charm.land/bubbletea/v2"
)

// maxCompletionItems bounds the popup so it never crowds out the transcript.
const maxCompletionItems = 8

// completion is the `/command` and `@file` popup over the input. It lives in
// its own type with its own update method so the Bubble Tea `Update` path stays
// inside the complexity budget.
//
// ponytail: it completes the trailing token of the input only, which is where
// the cursor sits while typing. Mid-line completion needs the textarea's cursor
// offset; add it when someone actually edits a prompt back-to-front.
type completion struct {
	items  []string
	cursor int
	// token is the trailing input token being completed, trigger included.
	token string
	// files caches the workspace file list, loaded on the first `@`.
	// ponytail: cached for the whole session, so files created after the first
	// `@` won't be suggested; invalidate after tool turns if that ever matters.
	files       []string
	filesLoaded bool
}

func (c *completion) active() bool { return len(c.items) > 0 }

func (c *completion) dismiss() { c.items, c.cursor, c.token = nil, 0, "" }

func (c *completion) refresh(value string) {
	token := trailingToken(value)
	switch {
	case strings.HasPrefix(token, "@"):
		c.items = matchItems(c.workspaceFiles(), strings.TrimPrefix(token, "@"), "@")
	case strings.HasPrefix(token, "/") && token == strings.TrimLeft(value, " \t"):
		// Slash commands are only a command at the very start of the prompt.
		// A bare "/" is the menu opening: re-derive dynamic commands so newly
		// installed skills appear without a restart.
		if token == "/" && RefreshDynamicCommands != nil {
			RefreshDynamicCommands()
		}
		c.items = matchItems(commandSuggestions(), token, "")
	default:
		c.dismiss()
		return
	}
	c.token = token
	if c.cursor >= len(c.items) {
		c.cursor = 0
	}
}

func (c *completion) selected(value string) string {
	if !c.active() {
		return value
	}
	return strings.TrimSuffix(value, c.token) + c.items[c.cursor] + " "
}

func (c *completion) workspaceFiles() []string {
	if c.filesLoaded {
		return c.files
	}
	c.filesLoaded = true
	root, err := os.Getwd()
	if err != nil {
		return nil
	}
	// Reuses file_search's walker (git ls-files, falling back to a walk) rather
	// than growing a second one.
	if files, err := file_search.ListCandidateFiles(context.Background(), root); err == nil {
		c.files = files
	}
	return c.files
}

// updateCompletionKey handles keys the popup owns while it is open, reporting
// whether the key was consumed.
func (m *model) updateCompletionKey(msg tea.KeyPressMsg) bool {
	if !m.completion.active() {
		return false
	}
	switch msg.Key().Code {
	case tea.KeyEscape:
		m.completion.dismiss()
	case tea.KeyTab:
		m.input.SetValue(m.completion.selected(m.input.Value()))
		m.input.CursorEnd()
		m.completion.dismiss()
	case tea.KeyUp:
		m.completion.move(-1)
	case tea.KeyDown:
		m.completion.move(1)
	default:
		return false
	}
	return true
}

func (c *completion) move(delta int) { c.cursor = (c.cursor + delta + len(c.items)) % len(c.items) }

func (c *completion) view() string {
	if !c.active() {
		return ""
	}
	rows := toolcore.MapSlice(c.items, func(item string) []string { return []string{item, commandSummary(item)} })
	widths := tuikit.Columns(rows)

	var b strings.Builder
	for i, row := range rows {
		style, marker := ui.Invokr.Muted, "  "
		if i == c.cursor {
			// The selection is the one place the design paints a background:
			// a foreground shift alone does not read as "this is the row".
			style, marker = ui.Invokr.Info.Background(ui.Invokr.SelectedBg), ui.Invokr.Info.Render("› ")
		}
		line := marker + style.Render(tuikit.Pad(row[0], widths[0]))
		if row[1] != "" {
			line += "  " + ui.Invokr.Muted.Render(row[1])
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(ui.Invokr.Muted.Render(fmt.Sprintf("  ↑/↓ select · tab complete · esc dismiss · %d", len(c.items))))
	return b.String()
}

func commandSummary(item string) string {
	name, slash := strings.CutPrefix(item, "/")
	if spec := findCommand(name); slash && spec != nil {
		return spec.summary
	}
	return ""
}

func trailingToken(value string) string {
	if value == "" || strings.ContainsAny(value[len(value)-1:], " \t\n") {
		return ""
	}
	return value[strings.LastIndexAny(value, " \t\n")+1:]
}

// matchItems returns up to maxCompletionItems candidates containing query,
// case-insensitively, each rendered with the given trigger prefix. ponytail:
// substring match in source order — no ranking until someone misses one.
func matchItems(candidates []string, query, trigger string) []string {
	query = strings.ToLower(query)
	var out []string
	for _, candidate := range candidates {
		if !strings.Contains(strings.ToLower(candidate), query) {
			continue
		}
		if out = append(out, trigger+candidate); len(out) == maxCompletionItems {
			break
		}
	}
	return out
}
