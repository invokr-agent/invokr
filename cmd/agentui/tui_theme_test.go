package agentui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-ai/invokr/surface/ui"
)

// The palette is read across both grounds: NewTheme picks one set per terminal,
// and the assertion must not depend on which argument means which ground.
func TestThinkingIsItsOwnGrey(t *testing.T) {
	a, b := ui.NewTheme(true), ui.NewTheme(false)
	brand := ui.Hex(a.Palette.Brand) + " " + ui.Hex(b.Palette.Brand)
	blue := ui.Hex(a.Palette.Blue) + " " + ui.Hex(b.Palette.Blue)
	think := ui.Hex(a.Thinking.GetForeground()) + " " + ui.Hex(b.Thinking.GetForeground())
	if !strings.Contains(brand, "#5b86ea") || !strings.Contains(blue, "#9a72ea") {
		t.Fatalf("dark brand/blue not lightened: %s / %s", brand, blue)
	}
	if !strings.Contains(think, "#8089a0") ||
		ui.Hex(a.Thinking.GetForeground()) == ui.Hex(a.Palette.Muted) {
		t.Fatalf("thinking has no grey of its own: %s vs muted %s", think, ui.Hex(a.Palette.Muted))
	}
}

func TestThinkingGutterIsThinkingColour(t *testing.T) {
	var b strings.Builder
	writeMessage(&b, message{role: "reasoning", text: "one thought"}, 60, "")
	got := b.String()
	if !strings.Contains(got, ui.Invokr.Thinking.Render(thinkingGutter)) {
		t.Fatalf("gutter not in the thinking colour: %s", got)
	}
}

func TestStatusRowKeepsOneAccent(t *testing.T) {
	s := statusline{folder: "GolandProjects/invokr", branch: "dev"}
	row := s.view("ornith-9b", "high", "auto", 120)
	for _, value := range []string{s.folder, s.branch, "ornith-9b", "high"} {
		if !strings.Contains(row, ui.Invokr.Muted.Render(value)) {
			t.Fatalf("cell %s is not grey:\n%s", value, row)
		}
	}
	if !strings.Contains(row, ui.Invokr.Warn.Render("auto")) {
		t.Fatal("the mode cell lost its accent")
	}
}

func TestInlineCodeHasNoPadding(t *testing.T) {
	got := strings.Join(strings.Fields(ansi.Strip(renderMarkdown("Div `x`.", 40))), " ")
	if !strings.Contains(got, "x.") {
		t.Fatalf("inline code keeps its padding: %q", got)
	}
}
