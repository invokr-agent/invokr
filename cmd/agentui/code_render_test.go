package agentui

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-agent/invokr/surface/ui"
)

var colorRuns = regexp.MustCompile(`\x1b\[38;[25];[0-9;]+m`)

// colourRuns lists the distinct foreground colours one rendered line carries, so
// a test can say "this line is still coloured" without naming chroma's palette.
func colourRuns(line string) []string {
	var runs []string
	for _, run := range colorRuns.FindAllString(line, -1) {
		if !slices.Contains(runs, run) {
			runs = append(runs, run)
		}
	}
	return runs
}

// colourOnly lists the colours a line carries beyond the ones in other.
func colourOnly(line, other string) []string {
	var only []string
	for _, run := range colourRuns(line) {
		if !slices.Contains(colourRuns(other), run) {
			only = append(only, run)
		}
	}
	return only
}

const thinkingSource = "Compare the branch first.\n\n```diff\n+added line\n-removed line\n```\n\nplain prose after"

func thinkingLinesAt(width int) []string {
	var b strings.Builder
	writeMessage(&b, message{role: "reasoning", text: thinkingSource}, width, "")
	return strings.Split(b.String(), "\n")
}

func lineWith(lines []string, needle string) string {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// TestReasoningCodeKeepsMutedHighlighting: reasoning is dimmed as one block, but
// fenced code inside it keeps a reduced-intensity version of its own colours —
// diff +/- lines stay red and green, and stay apart from each other.
func TestReasoningCodeKeepsMutedHighlighting(t *testing.T) {
	lines := thinkingLinesAt(80)
	added, removed := lineWith(lines, "+added"), lineWith(lines, "-removed")
	if added == "" || removed == "" {
		t.Fatalf("fenced code lost from thinking:\n%s", strings.Join(lines, "\n"))
	}
	// The fence characters are laid out, not printed.
	if strings.Contains(ansi.Strip(added), "```") || !strings.Contains(ansi.Strip(added), "+added line") {
		t.Fatalf("fence survived or code was cut: %q", added)
	}
	addedOwn, removedOwn := colourOnly(added, removed), colourOnly(removed, added)
	if len(addedOwn) == 0 || len(removedOwn) == 0 {
		t.Fatalf("+ and - lines share their colours:\n%q\n%q", added, removed)
	}
	// Muted: drawn faint, so it reads as code without competing with the answer.
	if !strings.Contains(added, "\x1b[2m") || !strings.Contains(added, "\x1b[0;2m") {
		t.Fatalf("code in thinking is not drawn faint: %q", added)
	}
}

// TestReasoningProseStaysPlainDim: prose keeps the thinking colour and takes no
// syntax colour of its own, so code and narration stay distinguishable.
func TestReasoningProseStaysPlainDim(t *testing.T) {
	lines := thinkingLinesAt(80)
	prose, code := lineWith(lines, "plain prose"), lineWith(lines, "+added")
	if prose == "" {
		t.Fatal("prose lost from thinking")
	}
	thinking := colourRuns(ui.Invokr.Thinking.Render("x"))
	for _, run := range colourRuns(prose) {
		if !slices.Contains(thinking, run) {
			t.Fatalf("prose carries a colour of its own (%q, thinking is %q): %q", run, thinking, prose)
		}
	}
	if len(colourOnly(code, prose)) == 0 {
		t.Fatalf("code carries no colour prose does not: %q", code)
	}
}

// TestSyntaxThemeRepaintsSettledCode: the highlighter is rebuilt with the palette,
// so a settled code block already on screen follows a theme change.
func TestSyntaxThemeRepaintsSettledCode(t *testing.T) {
	t.Cleanup(func() { setSyntaxTheme("") })
	before := thinkingLinesAt(80)
	setSyntaxTheme("catppuccin-latte")
	defer setSyntaxTheme("")
	after := thinkingLinesAt(80)
	if lineWith(before, "+added") == lineWith(after, "+added") {
		t.Fatal("reasoning code did not follow the syntax theme")
	}
}

// bigFileDiff is one file's unified diff with no body: the tail below grows it
// past the collapsed preview cap.
const bigFileDiff = "diff --git a/big.go b/big.go\nindex e69de29..4c5fd91 100644\n--- a/big.go\n+++ b/big.go\n@@ -0,0 +1,60 @@\n"

// TestShellDiffRendersThroughTheDiffView: git diff run through the shell is drawn
// like an applied edit — a per-file header over one tinted column — and the
// collapsed form keeps the preview cap while the expanded form drops it.
func TestShellDiffRendersThroughTheDiffView(t *testing.T) {
	long := bigFileDiff + strings.Repeat("+tail line\n", maxDiffLines+5)
	m := message{
		role: "tool", toolName: "shell", toolStatus: toolDone,
		toolArgs: `{"command":"git diff"}`, toolOutput: long,
	}
	if !isUnifiedDiff(shellText(long)) {
		t.Fatal("shell diff output not detected as a diff")
	}
	collapsed := ansi.Strip(renderSettledBlock(m, 80))
	if !strings.Contains(collapsed, "big.go +") || !strings.Contains(collapsed, "tail line") {
		t.Fatalf("collapsed shell diff lacks the diff view:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "diff --git") || strings.Contains(collapsed, "@@") {
		t.Fatalf("raw diff markers survived:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "more line") || strings.Count(collapsed, "tail line") >= maxDiffLines {
		t.Fatalf("collapsed shell diff is not a folded summary:\n%s", collapsed)
	}
	// The expanded form drops the cap. A different path keeps its DiffView render
	// cache apart, so the collapsed cap cannot leak into this render.
	m.toolOutput = strings.ReplaceAll(long, "big.go", "bigger.go")
	expanded := ansi.Strip(rendererFor("shell").Detail(m, 80))
	if strings.Count(expanded, "tail line") != maxDiffLines+5 || strings.Contains(expanded, "more line") {
		t.Fatalf("expanded shell diff not shown in full:\n%s", expanded)
	}
	// Ordinary output is untouched: no diff view, still a tail with a fold.
	m.toolOutput = "l1\nl2\nl3\nl4\nl5\nl6\nl7"
	if isUnifiedDiff(shellText(m.toolOutput)) {
		t.Fatal("plain output read as a diff")
	}
	got := ansi.Strip(renderSettledBlock(m, 60))
	if !strings.Contains(got, "l7") || !strings.Contains(got, "▸ 2 more lines") || strings.Contains(got, "l2") {
		t.Fatalf("plain shell tail changed:\n%s", got)
	}
}

// TestToolResultCodeIsHighlighted: a settled result's code is set apart by
// indentation and coloured by the language the tool names — a file extension
// from its path argument, a fence info string from the text itself.
func TestToolResultCodeIsHighlighted(t *testing.T) {
	var b strings.Builder
	writeMessage(&b, message{
		role: "tool", toolName: "read", toolStatus: toolDone, expanded: true,
		toolArgs: `{"path":"calc.go"}`, toolOutput: "func Mul(a, b int) int {",
	}, 80, "")
	lines := strings.Split(b.String(), "\n")
	code := lineWith(lines, "Mul")
	if code == "" {
		t.Fatalf("result text lost:\n%s", strings.Join(lines, "\n"))
	}
	if len(colourRuns(code)) == 0 {
		t.Fatalf("result code took no highlight from its file extension:\n%q", code)
	}
	// A fenced result from a tool that names no file is highlighted by the fence.
	writeMessage(&b, message{
		role: "tool", toolName: "web_fetch", toolStatus: toolDone, expanded: true,
		toolOutput: "```rust\nlet x = 1\n```",
	}, 80, "")
	fenced := strings.Split(b.String(), "\n")
	body := lineWith(fenced, "let")
	if body == "" || len(colourRuns(body)) == 0 {
		t.Fatalf("fenced result not highlighted:\n%s", strings.Join(fenced, "\n"))
	}
	if strings.Contains(ansi.Strip(body), "```") {
		t.Fatalf("fence characters survived:\n%s", strings.Join(fenced, "\n"))
	}
}

// TestHugeResultFallsBackToPlainText: the size cap keeps a megabyte of output
// legible and cheap instead of lexing it every frame.
func TestHugeResultFallsBackToPlainText(t *testing.T) {
	huge := strings.Repeat("func huge() {} x\n", maxCodeBytes/17+1)
	m := message{role: "tool", toolName: "read", toolStatus: toolDone,
		toolArgs: `{"path":"huge.go"}`, toolOutput: huge}
	text, file := resultBlock(m)
	if file != "huge.go" || text == "" {
		t.Fatalf("resultBlock = %q, want the path", file)
	}
	// Over the cap the block keeps one colour — the plain muted text it had before.
	if len(colourRuns(withOutputBlock("line", text, file, 80, 4))) > 1 {
		t.Fatalf("oversized result was highlighted: %q", colourRuns(withOutputBlock("line", text, file, 80, 4)))
	}
	if !strings.Contains(withOutputBlock("line", text, file, 80, 4), "func huge() {}") {
		t.Fatal("oversized result rendered nothing")
	}
	// Under the cap the same kind of block picks up syntax colours as well.
	if len(colourRuns(withOutputBlock("line", "func tiny() {}", "tiny.go", 80, 4))) < 2 {
		t.Fatal("small result lost its highlight")
	}
}
