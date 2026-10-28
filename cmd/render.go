package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/spf13/cobra"
	"io"
	"os"
	"sync"
	"time"

	"github.com/invokr-agent/invokr/surface/ui"
	toolcore "github.com/invokr-agent/invokr/tools/core"

	"charm.land/lipgloss/v2"
	"github.com/antonikliment/tuikit"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// tableGap is the space between columns: enough to read as a gap, narrow
// enough that a five-column table still fits an 80-column terminal.
const tableGap = 2

// cellWidth caps one cell. A session title is free text and a long one pushes
// every other column off the right edge, so it is cut with a trailing "…".
const cellWidth = 44

// renderer is the CLI's presentation surface. It exists so command output and
// the TUI cannot drift: both draw from surface/ui's palette, and neither picks
// a color of its own.
//
// Painting is decided once, per destination writer, by tuikit.PainterFor —
// a pipe or NO_COLOR gets the same text without escapes, so `invokr sessions |
// grep` and `invokr sessions > file` stay parseable. Structure (columns,
// alignment, the words themselves) is identical either way; only color is
// dropped. Anything a script should parse has --json instead.
type renderer struct {
	paint tuikit.Painter
	theme ui.Theme
}

func newRenderer(w io.Writer) renderer {
	if !tuikit.ColorEnabled(w) {
		// No escapes will be emitted, so the palette is never read — and the
		// terminal round trip that picks it is skipped with it.
		return renderer{paint: tuikit.Plain, theme: ui.Invokr}
	}
	return renderer{paint: tuikit.Paint, theme: paletteForTerminal()}
}

// paletteForTerminal asks the terminal which background it is drawing on and
// builds the palette for it, falling back to dark — the assumption the TUI's
// own default already makes.
//
// Asked once per process, because asking writes a query escape and reads the
// reply: a command that rendered two blocks would otherwise probe twice, and
// on a terminal that does not answer, each probe is a stall and a stray escape
// in the output.
var paletteForTerminal = sync.OnceValue(func() ui.Theme {
	// The query needs a real terminal on both ends — there is nobody to answer
	// it otherwise, and the request itself would land in a redirected stdout.
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return ui.Invokr
	}
	return ui.NewTheme(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
})

func (r renderer) muted(s string) string   { return r.paint(r.theme.Muted, s) }
func (r renderer) accent(s string) string  { return r.paint(r.theme.Accent, s) }
func (r renderer) success(s string) string { return r.paint(r.theme.Success, s) }
func (r renderer) danger(s string) string  { return r.paint(r.theme.Danger, s) }
func (r renderer) warn(s string) string    { return r.paint(r.theme.Warn, s) }

// table renders aligned columns with a muted header. Long cells end in "…"
// (ANSI-aware), and styled cells still align because tuikit
// measures display width rather than bytes.
func (r renderer) table(header []string, rows [][]string) string {
	for _, row := range rows {
		for i, cell := range row {
			row[i] = ansi.Truncate(cell, cellWidth, "…")
		}
	}
	return r.theme.Palette.Table(r.paint, header, rows, tableGap)
}

// status paints a state word by what it means — "ok" green, "failed" red,
// anything unfamiliar amber, because an unrecognized state is one worth
// reading. The grading lives in tuikit so the TUI and the CLI agree on which
// words are fine.
func (r renderer) status(word string) string { return r.theme.Palette.StatusWord(r.paint, word) }

// policy paints an approval policy by its risk rather than by its wording:
// tuikit would grade "allow" as a good outcome, and on a scheduled job it is
// the opposite — every tool call approved with nobody watching.
func (r renderer) policy(p string) string {
	switch p {
	case approval.PolicyAllowAll:
		return r.danger(p)
	case approval.PolicyDeny:
		return r.muted(p)
	}
	return r.warn(p) // a tier list: some calls run unattended
}

// when renders an instant as how long ago it was, which is the question a
// listing answers; older than a day it falls back to the absolute time.
func (r renderer) when(at time.Time) string {
	if at.IsZero() {
		return r.muted("-")
	}
	return tuikit.Age(at, time.Now())
}

func addJSONFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
}

func emit(cmd *cobra.Command, v any, table func() error) error {
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
	}
	return table()
}

// renderTable prints a table of items, or a muted note when there are none.
// The table already ends in a newline.
func renderTable[T any](out io.Writer, header []string, empty string, items []T, row func(renderer, T) []string) error {
	r := newRenderer(out)
	if len(items) == 0 {
		_, err := fmt.Fprintln(out, r.muted(empty))
		return err
	}
	rows := toolcore.MapSlice(items, func(item T) []string { return row(r, item) })
	_, err := fmt.Fprint(out, r.table(header, rows))
	return err
}
