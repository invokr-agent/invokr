package agentui

import (
	"cmp"
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
	codeHighlighter = tuikit.ChromaHighlighter(chromaTheme())
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

// maxCodeBytes is the largest settled code block fed through chroma; a bigger
// block falls back to plain text rather than lexing megabytes every frame.
const maxCodeBytes = 64 * 1024

// codeHighlighter colors one line of code, its lexer picked from the filename's
// extension (see tuikit.Highlighter). Rebuilt by resetMarkdown, so a palette or
// syntax-theme change repaints settled code already on screen.
var codeHighlighter tuikit.Highlighter

// fenceExtensions names the file extension chroma registers a fence language
// under, for the languages whose name is not that extension.
var fenceExtensions = map[string]string{
	"rust": "rs", "python": "py", "javascript": "js", "typescript": "ts",
	"shell": "sh", "bash": "sh", "console": "sh", "terminal": "sh",
	"text": "txt", "plain": "txt", "output": "txt", "diff": "diff", "patch": "diff",
}

// codeFileFor names a file chroma will pick a lexer for from a fence language.
// An unknown language still reaches chroma, which degrades to plain text rather
// than failing, so a made-up fence label costs colors, not legibility.
func codeFileFor(language string) string {
	if language == "" {
		return ""
	}
	name := strings.ToLower(language)
	return "code." + cmp.Or(fenceExtensions[name], name)
}

// codeStyle is the per-line style a settled code block is drawn with: chroma's
// colors, muted inside reasoning. An unknown language, or a block over maxCodeBytes,
// stays the plain muted text it was before.
func codeStyle(file string, muted bool) func(string) string {
	if file == "" {
		return func(line string) string { return ui.Invokr.Muted.Render(line) }
	}
	return func(line string) string {
		highlighted := codeHighlighter(file, line)
		if muted {
			return muteCode(highlighted)
		}
		return highlighted
	}
}

// muteCode dims highlighted code with the terminal's faint attribute, which
// keeps each colour's hue. Chroma resets after every token, so faint is
// re-applied after each reset — either spelling of the reset ([0m or the
// bare [m) drops faint, so both get it back.
func muteCode(text string) string {
	reset := strings.ReplaceAll(strings.ReplaceAll(text, "\x1b[0m", "\x1b[0;2m"), "\x1b[m", "\x1b[;2m")
	return "\x1b[2m" + reset + "\x1b[0m"
}
