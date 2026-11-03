package agentui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/surface/ui"
	toolcore "github.com/invokr-agent/invokr/tools/core"

	"github.com/antonikliment/tuikit"

	"github.com/charmbracelet/x/ansi"
)

// blockRenderer renders the body of a settled tool block in the transcript.
// Summary is the collapsed form (the default); Detail is the expanded form a
// fold toggle reveals. Both compose on top of settledToolLine, which is where
// the outcome ledger's status flags (toolStatus/toolCode) become the glyph —
// renderers never infer success or failure from output text.
type blockRenderer interface {
	Summary(m message, width int) string
	Detail(m message, width int) string
}

// toolRenderers maps a tool name to its dedicated renderer. Unknown tools —
// toolbox scripts, MCP servers, anything not listed — fall back to the generic
// renderer, which is the pre-seam rendering unchanged.
var toolRenderers = map[string]blockRenderer{
	"shell": shellRenderer{}, "apply_patch": patchRenderer{}, "edit": patchRenderer{}, "todo": todoRenderer{},
	"git_read": gitReadRenderer{},
}

func rendererFor(name string) blockRenderer {
	return cmp.Or(toolRenderers[name], blockRenderer(genericRenderer{}))
}

func renderSettledBlock(m message, width int) string {
	r := rendererFor(m.toolName)
	if m.expanded {
		return r.Detail(m, width)
	}
	return r.Summary(m, width)
}

// genericRenderer is the fallback: the tool line plus the failure sub-block —
// exactly what every tool block looked like before the seam existed.
type genericRenderer struct{}

func (genericRenderer) Summary(m message, width int) string {
	line := settledToolLine(m, width)
	if text := strings.TrimRight(m.toolOutput, "\n"); text != "" {
		line += "\n" + foldIndicator(strings.Count(text, "\n")+1, width)
	}
	return line
}

func (genericRenderer) Detail(m message, width int) string {
	text, file := resultBlock(m)
	return withOutputBlock(settledToolLine(m, width), text, file, width, 0)
}

// shellRenderer shows the command line (exit glyph from the ledger flags) and
// a short tail of the recorded output, so the result of a command is visible
// without unfolding it.
type shellRenderer struct{}

// shellTailLines is how many trailing output lines a collapsed shell block
// shows. The rest hides behind the fold indicator.
const shellTailLines = 5

// A shell result that parses as a unified diff gets the git diff renderer rather
// than a text tail: `git diff`, `git show` and `diff -u` run through the shell
// then read like the native git tool and like an applied edit, with the same
// preview cap when collapsed and the whole diff when expanded.
func (shellRenderer) Summary(m message, width int) string {
	line := settledToolLine(m, width)
	text := shellText(m.toolOutput)
	if diff := shellDiffText(text); diff != "" {
		return line + "\n" + renderGitDiff(diff, width, maxDiffLines)
	}
	tail, hidden := tailLines(text, shellTailLines)
	if hidden > 0 {
		line += "\n" + foldIndicator(hidden, width)
	}
	return withOutputBlock(line, strings.Join(tail, "\n"), commandFile(m.toolArgs), width, 0)
}

func (shellRenderer) Detail(m message, width int) string {
	line := settledToolLine(m, width)
	text := shellText(m.toolOutput)
	if diff := shellDiffText(text); diff != "" {
		return line + "\n" + renderGitDiff(diff, width, 0)
	}
	return withOutputBlock(line, text, commandFile(m.toolArgs), width, 0)
}

// isUnifiedDiff reports whether recorded output parses as a unified diff, which
// is what tuikit's DiffView needs to number and colour it per file.
func isUnifiedDiff(text string) bool {
	return len(tuikit.ParseUnifiedDiff(text)) > 0
}

// shellText unwraps the shell tool's recorded JSON envelope into the streams a
// human wants to read — stdout then stderr. Anything that is not the envelope
// (e.g. an error payload) is shown as recorded.
func shellText(output string) string {
	var res struct {
		Stdout *string `json:"stdout"`
		Stderr string  `json:"stderr"`
	}
	if err := json.Unmarshal([]byte(output), &res); err != nil || res.Stdout == nil {
		return plainTokfMarker(exitCodeLine.ReplaceAllString(output, ""))
	}
	streams := []string{strings.TrimRight(*res.Stdout, "\n"), strings.TrimRight(res.Stderr, "\n")}
	return plainTokfMarker(strings.Join(slices.DeleteFunc(streams, func(s string) bool { return s == "" }), "\n"))
}

// exitCodeLine is the shell tool's "[exit code N]" fact for the model. The
// block's status line already shows the exit, so the human copy drops it.
var exitCodeLine = regexp.MustCompile(`(?m)\n?^\[exit code -?\d+\]$`)

// patchRenderer owns the diff sub-block under a writing tool: apply_patch
// records its patch text, edit rebuilds a patch-shaped diff from the call's
// arguments (see editedFileDiff), and both render through the same DiffView.
// Collapsed it keeps the maxDiffLines preview cap; expanded it shows the whole
// recorded diff.
type patchRenderer struct{}

func (patchRenderer) Summary(m message, width int) string {
	line := settledToolLine(m, width)
	if m.toolDiff == "" {
		return line
	}
	return line + "\n" + renderEditDiff(m.editFile, m.toolDiff, width, maxDiffLines)
}

func (patchRenderer) Detail(m message, width int) string {
	line := settledToolLine(m, width)
	if m.toolDiff == "" {
		return withOutputBlock(line, m.toolOutput, "", width, 0)
	}
	return line + "\n" + renderEditDiff(m.editFile, m.toolDiff, width, 0)
}

// gitReadRenderer renders git_read results. A diff/show envelope carries the
// full diff even when the model-facing copy was spilled (the completed event
// records the ungated output), so the human always sees the whole thing:
// collapsed shows the stat summary, expanded renders the diff through
// DiffView. Non-diff ops fall back to the plain output text.
type gitReadRenderer struct{}

func (gitReadRenderer) Summary(m message, width int) string {
	line := settledToolLine(m, width)
	stat, diff, plain := gitReadParts(m.toolOutput)
	if diff == "" {
		return withOutputBlock(line, plain, "", width, maxDiffLines)
	}
	if hidden := strings.Count(diff, "\n") + 1; hidden > 0 {
		line += "\n" + foldIndicator(hidden, width) + " " + ui.Invokr.Muted.Render("(diff)")
	}
	return withOutputBlock(line, stat, "", width, maxDiffLines)
}

func (gitReadRenderer) Detail(m message, width int) string {
	line := settledToolLine(m, width)
	_, diff, plain := gitReadParts(m.toolOutput)
	if diff == "" {
		return withOutputBlock(line, plain, "", width, 0)
	}
	return line + "\n" + renderGitDiff(diff, width, 0)
}

// gitReadParts unwraps a git_read envelope: {stat, diff} for diff/show,
// {output} for the text ops. Anything else renders as recorded.
func gitReadParts(output string) (stat, diff, plain string) {
	var res struct {
		Stat   string  `json:"stat"`
		Diff   *string `json:"diff"`
		Output *string `json:"output"`
	}
	err := json.Unmarshal([]byte(output), &res)
	if err != nil {
		// Result filters (tokf) may wrap the envelope in decoration lines;
		// retry on the first JSON object found in the text.
		err = json.Unmarshal([]byte(strutil.ExtractJSONObject(output)), &res)
	}
	if err != nil {
		return "", "", output
	}
	if res.Diff != nil {
		return res.Stat, *res.Diff, ""
	}
	if res.Output != nil {
		return "", "", *res.Output
	}
	return "", "", output
}

// todoRenderer renders the task list a todo call recorded, one checklist glyph
// per item, from the call's own arguments (the complete replacement list).
type todoRenderer struct{}

func (t todoRenderer) Summary(m message, width int) string {
	line := settledToolLine(m, width)
	items := todoItemsFromArgs(m.toolArgs)
	if len(items) == 0 {
		return line
	}
	rows := toolcore.MapSlice(items, func(item todoArgItem) string {
		return "    " + todoGlyph(item.Status) + " " + ui.Invokr.Muted.Render(ansi.Truncate(item.Task, max(1, width-8), "…"))
	})
	return line + "\n" + strings.Join(rows, "\n")
}

func (t todoRenderer) Detail(m message, width int) string { return t.Summary(m, width) }

type todoArgItem struct {
	Task   string `json:"task"`
	Status string `json:"status"`
}

func todoItemsFromArgs(argsJSON string) []todoArgItem {
	var fields struct {
		Items []todoArgItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &fields); err != nil {
		return nil
	}
	return fields.Items
}

// todoGlyph is the checklist mark per status: done green, in-progress the
// running blue, pending dim.
func todoGlyph(status string) string {
	switch status {
	case "done":
		return ui.Invokr.Success.Render(glyphOK)
	case "in_progress":
		return ui.Invokr.Info.Render("◐")
	default:
		return ui.Invokr.Muted.Render("○")
	}
}

// tailLines returns the last n lines of text plus how many were hidden.
func tailLines(text string, n int) ([]string, int) {
	if text = strings.TrimRight(text, "\n"); text == "" {
		return nil, 0
	}
	lines := strings.Split(text, "\n")
	n = min(n, len(lines))
	return lines[len(lines)-n:], len(lines) - n
}

func foldIndicator(hidden, width int) string {
	return "    " + ui.Invokr.Muted.Render(ansi.Truncate("▸ "+moreLines(hidden), max(1, width-4), "…"))
}

func withOutputBlock(line, output, file string, width, maxLines int) string {
	if output = strings.TrimRight(output, "\n"); output == "" {
		return line
	}
	if len(output) > maxCodeBytes {
		file = "" // too big to lex every frame: plain text is the fallback
	}
	return line + "\n" + strings.Join(cappedLines(output, width, maxLines, codeStyle(file, false)), "\n")
}

// resultBlock unwraps a settled tool result into the text to show and the file
// its code should be highlighted as: the tool's own path argument when it names
// one, otherwise the info string of a code fence wrapping the result. Neither
// known leaves the block plain muted text, exactly as before.
func resultBlock(m message) (text, file string) {
	text, file = m.toolOutput, outputFile(m.toolArgs)
	var lang string
	if text, lang = stripFence(text); lang != "" {
		file = cmp.Or(file, codeFileFor(lang))
	}
	return text, file
}

// outputFile names the file a tool result belongs to, so chroma picks the lexer
// from its extension. Tools spell the argument several ways.
func outputFile(argsJSON string) string {
	var fields struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Filename string `json:"filename"`
		File     string `json:"file"`
	}
	if json.Unmarshal([]byte(argsJSON), &fields) != nil {
		return ""
	}
	// Any non-empty name reaches chroma: it matches whole filenames too, so an
	// extensionless Makefile or Dockerfile is worth trying rather than dropping.
	return cmp.Or(fields.Path, fields.FilePath, fields.Filename, fields.File)
}

// stripFence drops a wrapping code fence and reports its info string ("" when
// the text is not fenced), so a fenced result is highlighted rather than showing
// backticks.
func stripFence(text string) (string, string) {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return text, ""
	}
	open, lang := fenceMarker(lines[0])
	last, _ := fenceMarker(lines[len(lines)-1])
	if open == "" || last != open {
		return text, ""
	}
	return strings.Join(lines[1:len(lines)-1], "\n"), lang
}

// cappedLines indents and styles each line of text, truncating to the pane
// width and capping the count (0 = uncapped) with a fold indicator.
func cappedLines(text string, width, maxLines int, style func(string) string) []string {
	lines := strings.Split(text, "\n")
	truncated := 0
	if maxLines > 0 && len(lines) > maxLines {
		truncated, lines = len(lines)-maxLines, lines[:maxLines]
	}
	for i, l := range lines {
		lines[i] = "    " + style(ansi.Truncate(l, max(1, width-4), "…"))
	}
	if truncated > 0 {
		lines = append(lines, foldIndicator(truncated, width))
	}
	return lines
}

func moreLines(n int) string {
	if n == 1 {
		return "1 more line"
	}
	return fmt.Sprintf("%d more lines", n)
}

// shellDiffText returns the unified-diff region of recorded shell output, or
// "" when it has none. The text reaches the transcript with the result filter's
// decoration in it — a marker line, and a filler line where the middle of a
// long result was cut — which ParseUnifiedDiff rejects, so a real `git diff |
// head -30` fell back to a dimmed tail. Starting at the first header line and
// dropping the filler leaves the hunks intact.
func shellDiffText(text string) string {
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if diffHeaderLine.MatchString(l) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	var kept []string
	for _, l := range lines[start:] {
		if !filterFiller.MatchString(l) {
			kept = append(kept, l)
		}
	}
	region := strings.Join(kept, "\n")
	if !isUnifiedDiff(region) {
		return ""
	}
	return region
}

var diffHeaderLine = regexp.MustCompile(`^(diff --git |index [0-9a-f]{6,}|--- [ab]/|--- /dev/null)`)
var filterFiller = regexp.MustCompile(`^(compressed — run .tokf raw \d+.|--- \d+ lines omitted|… truncated)`)

// plainTokfMarker drops the 🗜️ glyph tokf (an external output filter) puts
// in front of its "compressed" notes: terminals disagree on its width, which
// pushes stray cells past overlay borders. The words stay.
func plainTokfMarker(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, tokfMarker+" ", ""), tokfMarker, "")
}

const tokfMarker = "\U0001F5DC\uFE0F"

// commandFile names the file a printing command read (cat/head/tail/sed -n),
// so its recorded output is highlighted as that file instead of muted prose.
func commandFile(argsJSON string) string {
	var fields struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(argsJSON), &fields) != nil {
		return ""
	}
	return printedFile(fields.Command)
}

var printers = map[string]bool{"cat": true, "head": true, "tail": true, "sed": true, "bat": true, "nl": true, "less": true, "more": true}

func printedFile(command string) string {
	for _, segment := range strings.Split(command, "|") {
		words := strings.Fields(segment)
		if len(words) == 0 || !printers[words[0]] {
			continue
		}
		for _, w := range words[1:] {
			if w[0] == '-' || w[0] == '+' || w[0] == '\'' || w[0] == '"' || w[0] == '$' || w[0] == '*' || strings.ContainsAny(w, "<>*;&") {
				continue
			}
			return w
		}
	}
	return ""
}
