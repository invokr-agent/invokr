package agentui

import (
	"strings"

	"github.com/invokr-agent/invokr/surface/ui"

	"github.com/antonikliment/tuikit"
	"github.com/antonikliment/tuikit/markdown"

	"charm.land/glamour/v2/ansi"
)

// The model emits real CommonMark — tables, fenced code, headings, block
// quotes — and every character of it used to reach the screen verbatim. These
// render it through tuikit's glamour-backed renderer, which maps ui.Invokr.Palette
// onto glamour's stylesheet; only the handful of elements the design speaks to
// beyond the palette are set here.

// renderMarkdown lays out CommonMark at the given width, and streamer renders
// the in-flight answer. Both are rebuilt by resetMarkdown when the palette
// changes; only one message streams at a time and the Bubble Tea loop is
// single-threaded, so one streamer carries the cache.
var (
	renderMarkdown tuikit.RenderFunc
	streamer       tuikit.StreamingMarkdown
)

// syntaxTheme is the chroma stylesheet fenced code is highlighted with. Empty
// means "match the terminal background", which is the default and what an
// unanswered setup question leaves behind. Set from config at startup and by the
// settings View tab's Syntax theme row.
var syntaxTheme string

func init() { resetMarkdown() }

// setSyntaxTheme records the choice and rebuilds against it. An unknown name is
// corrected by tuikit to its own default rather than rendering as broken
// highlighting, so a stale config entry costs colors, not legibility.
func setSyntaxTheme(name string) {
	syntaxTheme = name
	resetMarkdown()
}

// resetMarkdown rebuilds both against the current palette. Rebuilding the
// streamer as well is not optional: its cache is keyed on the source text and
// the width, so a settled block would keep the colors it was first drawn in —
// and a finished answer's source never changes again.
func resetMarkdown() {
	resetDiffViews()
	renderMarkdown = markdown.New(ui.Invokr.Palette, markdown.WithSyntaxTheme(chromaTheme()), markdown.WithStyleFunc(designStyle()))
	streamer = tuikit.NewStreamingMarkdown(renderMarkdown)
}

// designStyle carries the design past what a tuikit.Theme describes: a base
// body color, the chip fill behind inline code, and a quote rule drawn in the
// frame color.
func designStyle() func(*ansi.StyleConfig) {
	text := ui.Hex(ui.Invokr.Text.GetForeground())
	chip := ui.Hex(ui.Invokr.ChipBg)
	rule := ui.Invokr.Frame.Render("│ ")
	return func(s *ansi.StyleConfig) {
		s.Document.Color = &text
		s.Table.Color = &text
		s.CodeBlock.Color = &text
		s.BlockQuote.IndentToken = &rule
		// Inline code lands on the same chip fill the tool lines use.
		s.Code.BackgroundColor = &chip
		// No space padding around the chip: the chip's own fill separates the
		// code from prose, and padding leaves a gap before punctuation ("Div .").
		s.Code.Prefix, s.Code.Suffix = "", ""
	}
}

// chromaTheme picks the syntax-highlighting palette for fenced code: the user's
// choice if there is one, otherwise the closest match to the terminal's ground.
// chroma ships stylesheets rather than taking colors, so neither is a mapping of
// the palette. The names are chroma's own; markdown.SyntaxThemes lists them.
func chromaTheme() string {
	if syntaxTheme != "" {
		return syntaxTheme
	}
	if ui.Hex(ui.Invokr.ChipBg) == ui.Hex(ui.NewTheme(true).ChipBg) {
		return "tokyonight-night"
	}
	return "tokyonight-day"
}

// renderPartialMarkdown is the streaming path: settled blocks come through
// glamour, the unfinished tail is closed speculatively and rendered too, so
// markdown formats as it arrives instead of popping in at the end of the turn.
//
// The result is not wrapped in a lipgloss style: it already carries glamour's
// and chroma's own SGR runs, and a foreground applied over the top is
// re-asserted after every embedded reset, repainting what they colored.
func renderPartialMarkdown(text string, width int) string {
	return streamer.Render(strings.TrimSpace(text), width)
}
