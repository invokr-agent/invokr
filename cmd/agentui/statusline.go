package agentui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/invokr-ai/invokr/capability/agentprompt"
	"github.com/invokr-ai/invokr/surface/ui"

	"charm.land/lipgloss/v2"
)

// Statusline glyphs: an emoji folder, which reads at a glance even in a font
// without Nerd Font coverage, plus the Nerd Font git and microchip glyphs the
// tool icons already draw from.
const (
	glyphFolder = "\U0001F4C1" // 📁
	glyphBranch = "\ue725"     // git branch (Nerd Font, nf-dev-git_branch)
	glyphModel  = "\uf2db"     // microchip
	glyphEffort = "\u25d4"     // ◔ reasoning effort
	glyphMode   = "\uf023"     // lock (Nerd Font, nf-fa-lock): approval mode
)

// statusline is the row under the input: which directory the agent works in,
// which branch that checkout is on, and which model answers. Folder and branch
// are a startup snapshot — agentprompt.DetectEnvironment takes the same one for
// the system prompt, and a mid-session checkout is not worth a file read per
// frame. The model is read live, because /model switches it.
type statusline struct {
	folder, branch string
}

func newStatusline() statusline {
	root, err := os.Getwd()
	if err != nil {
		return statusline{}
	}
	env := agentprompt.DetectEnvironment(root)
	return statusline{folder: shortPath(env.Cwd), branch: env.Branch}
}

// shortPath is the last two segments of dir ("GolandProjects/mercury"). A bare
// basename is ambiguous the moment two checkouts share a name, and the absolute
// path spends the whole row on parents nobody reads.
func shortPath(dir string) string {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return ""
	}
	parent, base := filepath.Split(dir)
	if parent = filepath.Clean(parent); parent == "" || parent == "." || parent == string(filepath.Separator) {
		return base
	}
	return filepath.Join(filepath.Base(parent), base)
}

// view renders the row, dropping cells from the right until it fits: the folder
// is the one that says where a stray command would land, so it goes last.
func (s statusline) view(model, reasoning, mode string, width int) string {
	// The row is grey: it reports where the agent works and what answers, which
	// is metadata, not attention. Only the mode cell carries a colour, and it is
	// one accent, because the mode is the thing the user must see at a glance.
	cells := []string{cell(glyphMode, ui.Invokr.Warn, mode)}
	if s.folder != "" {
		cells = append(cells, cell(glyphFolder, ui.Invokr.Muted, s.folder))
	}
	if s.branch != "" {
		cells = append(cells, cell(glyphBranch, ui.Invokr.Muted, s.branch))
	}
	if model = strings.TrimSpace(model); model != "" {
		cells = append(cells, cell(glyphModel, ui.Invokr.Muted, model))
	}
	if reasoning != "" {
		cells = append(cells, cell(glyphEffort, ui.Invokr.Muted, reasoning))
	}
	fit := lipgloss.NewStyle().MaxWidth(max(1, width))
	sep := ui.Invokr.Muted.Render(" \u00b7 ")
	for len(cells) > 1 {
		row := strings.Join(cells, sep)
		if lipgloss.Width(row) <= width {
			return row
		}
		cells = cells[:len(cells)-1]
	}
	return fit.Render(cells[0])
}

func cell(glyph string, style lipgloss.Style, value string) string {
	return glyph + " " + style.Render(value)
}
