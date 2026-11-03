package agentui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// marker is tokf's prefix as it reaches recorded shell output.
const marker = tokfMarker + " "

// compressedDiff is a shell result as the shell tool records it once the result
// filter cut the middle out: the marker prefix, a filler line where lines were
// dropped, the surviving hunks, and the trailing "compressed" line.
const compressedDiff = marker + "diff --git a/cmd/agentui/markdown.go b/cmd/agentui/markdown.go\n" +
	"index 1111111..2222222 100644\n--- a/cmd/agentui/markdown.go\n+++ b/cmd/agentui/markdown.go\n@@ -1,4 +1,4 @@\n-package agentui\n+package agentui2\n \n import (\n" +
	"--- 40 lines omitted (read the spill file for the rest) ---\n" +
	"diff --git a/cmd/agentui/format.go b/cmd/agentui/format.go\nindex 3333333..4444444 100644\n--- a/cmd/agentui/format.go\n+++ b/cmd/agentui/format.go\n@@ -1,3 +1,3 @@\n-package agentui\n+package agentui3\n" +
	"[exit code 0]\n" + tokfMarker + "compressed — run `tokf raw 4216` for full output"

// TestFilteredShellDiffStillRendersAsADiff: a `git diff | head -30` run through
// the shell reaches the transcript with the result filter's decoration in it,
// which ParseUnifiedDiff rejects, so it used to fall back to a dimmed tail.
// Detection works on the recorded text: the filler and marker lines are dropped
// and the surviving hunks parse.
func TestFilteredShellDiffStillRendersAsADiff(t *testing.T) {
	if shellDiffText(shellText(compressedDiff)) == "" {
		t.Fatalf("filtered shell diff not detected:\n%s", shellText(compressedDiff))
	}
	m := message{
		role: "tool", toolName: "shell", toolStatus: toolDone,
		toolArgs: `{"command":"git diff cmd/agentui | head -30"}`, toolOutput: compressedDiff,
	}
	collapsed := ansi.Strip(renderSettledBlock(m, 80))
	if !strings.Contains(collapsed, "markdown.go +") || !strings.Contains(collapsed, "format.go +") {
		t.Fatalf("filtered shell diff did not render through the diff view:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "diff --git") || strings.Contains(collapsed, "index 1111111") {
		t.Fatalf("raw diff text survived:\n%s", collapsed)
	}
	// Ordinary output is still a tail with a fold, never a diff view.
	plain := message{
		role: "tool", toolName: "shell", toolStatus: toolDone, toolArgs: `{"command":"ls -l"}`,
		toolOutput: "total 12\ndrwxr-xr-x 2 u g 4096 Nov 1 cmd\n-rw-r--r-- 1 u g 12 Nov 1 f.go",
	}
	if shellDiffText(shellText(plain.toolOutput)) != "" {
		t.Fatalf("ordinary shell output read as a diff: %q", plain.toolOutput)
	}
	if !strings.Contains(ansi.Strip(renderSettledBlock(plain, 60)), "f.go") {
		t.Fatal("ordinary shell tail lost")
	}
}

// TestPrintedFileIsHighlightedAsItsSource: `cat`/`head`/`tail`/`sed -n` of a
// file is code, so the command names the file the block is highlighted as.
func TestPrintedFileIsHighlightedAsItsSource(t *testing.T) {
	for _, c := range []struct {
		command, want string
	}{
		{`cat cmd/agentui/markdown.go`, "cmd/agentui/markdown.go"},
		{`cat cmd/agentui/markdown.go | head -20`, "cmd/agentui/markdown.go"},
		{`head -40 cmd/agentui/tool_render.go`, "cmd/agentui/tool_render.go"},
		{`head -n 20 a.go`, "a.go"},
		{`tail -c 100 x.go`, "x.go"},
		{`tail -n +30 cmd/agentui/render.go`, "cmd/agentui/render.go"},
		{`sed -n '1,20p' cmd/agentui/format.go`, "cmd/agentui/format.go"},
		{`go test ./cmd/...`, ""},
		{`git diff cmd/agentui`, ""},
		{`cat -n Makefile`, "Makefile"},
	} {
		args := `{"command":"` + c.command + `"}`
		if got := commandFile(args); got != c.want {
			t.Fatalf("commandFile(%s) = %q, want %q", c.command, got, c.want)
		}
	}
	m := message{
		role: "tool", toolName: "shell", toolStatus: toolDone, expanded: true,
		toolArgs: `{"command":"cat cmd/agentui/markdown.go"}`, toolOutput: "func Mul(a, b int) int {",
	}
	code := lineWith(strings.Split(renderSettledBlock(m, 80), "\n"), "Mul")
	if len(colourRuns(code)) < 2 {
		t.Fatalf("printed file took no highlight from its name: %q", code)
	}
}

// TestReasoningFencedCodeHonoursTheSizeCap: settled code blocks are capped at
// maxCodeBytes; fenced reasoning now shares that cap, so an oversized block
// stays plain muted instead of being lexed every frame.
func TestReasoningFencedCodeHonoursTheSizeCap(t *testing.T) {
	huge := strings.Repeat("x = 1\n", maxCodeBytes/6+1)
	big := thinkingLines("```go\n"+huge+"```", 60)
	if len(big) == 0 {
		t.Fatal("fenced reasoning lost")
	}
	for _, l := range big {
		if len(colourRuns(l)) > 1 {
			t.Fatalf("oversized fenced reasoning was lexed: %q", l)
		}
	}
	if !strings.Contains(ansi.Strip(big[len(big)-1]), "x = 1") {
		t.Fatal("oversized fenced reasoning rendered nothing")
	}
	// Under the cap the same fence still picks up syntax colours.
	small := thinkingLines("```go\nvar x = 1\n```", 60)
	if len(colourRuns(lineWith(small, "var"))) == 0 {
		t.Fatalf("small fenced reasoning lost its highlight:\n%s", strings.Join(small, "\n"))
	}
}

// TestExtensionlessFileStillNames: chroma matches whole filenames too, so
// Makefile, Dockerfile and LICENSE are worth a lexer rather than dropped for
// having no dot.
func TestExtensionlessFileStillNames(t *testing.T) {
	for _, c := range []struct {
		args, want string
	}{
		{`{"path":"Makefile"}`, "Makefile"},
		{`{"file":"Dockerfile.e2e"}`, "Dockerfile.e2e"},
		{`{"filename":"LICENSE"}`, "LICENSE"},
		{`{"path":""}`, ""},
		{`{}`, ""},
	} {
		if got := outputFile(c.args); got != c.want {
			t.Fatalf("outputFile(%s) = %q, want %q", c.args, got, c.want)
		}
	}
	if codeStyle("Makefile", false)("all: build") == codeStyle("", false)("all: build") {
		t.Fatal("an extensionless name never reached the highlighter")
	}
}

// TestMuteCodeCoversBothResetSpellings: a bare "\x1b[m" resets faint just like
// "\x1b[0m", so faint has to be re-applied after either one.
func TestMuteCodeCoversBothResetSpellings(t *testing.T) {
	for _, c := range []struct {
		in, want string
	}{
		{"\x1b[38;2;1;2;3mconst\x1b[0m x", "\x1b[0;2m"},
		{"\x1b[38;2;1;2;3mconst\x1b[m x", "\x1b[;2m"},
	} {
		muted := muteCode(c.in)
		if !strings.Contains(muted, c.want) || strings.Count(muted, "2m") < 2 {
			t.Fatalf("faint lost after a reset (%q): %q", c.in, muted)
		}
	}
}

// TestFilterMarkerIsPlainTextInTheTranscript: the marker's words carry the
// meaning, and the glyph leaves stray cells past overlay borders.
func TestFilterMarkerIsPlainTextInTheTranscript(t *testing.T) {
	m := message{
		role: "tool", toolName: "shell", toolStatus: toolDone, expanded: true,
		toolOutput: "one\ntwo\n" + marker + "compressed — run `tokf raw 7` for full output",
	}
	block := renderSettledBlock(m, 80)
	if strings.Contains(block, "\U0001F5CA") {
		t.Fatalf("the filter glyph survived: %q", block)
	}
	if !strings.Contains(ansi.Strip(block), "compressed — run `tokf raw 7` for full output") {
		t.Fatalf("the marker's words were lost:\n%s", ansi.Strip(block))
	}
	if plainTokfMarker(marker+"one\ntwo") != "one\ntwo" {
		t.Fatal("the leading marker prefix was not dropped")
	}
}
