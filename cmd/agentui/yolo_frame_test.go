package agentui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/surface/ui"
)

// TestYoloFrameIsTheApprovalYellow: the frame is the approval yellow through a
// role of its own, so a theme can set the frame apart from a warning line
// without inventing a third yellow.
func TestYoloFrameIsTheApprovalYellow(t *testing.T) {
	theme := ui.NewTheme(true)
	if ui.Hex(theme.YoloFrame.GetForeground()) != ui.Hex(theme.Palette.Yellow) {
		t.Fatalf("yolo frame %s, want the palette yellow %s",
			ui.Hex(theme.YoloFrame.GetForeground()), ui.Hex(theme.Palette.Yellow))
	}
}

// TestYoloFrameRulesTheWholeUI: an unprompted session is ruled on top and both
// sides (not the bottom), and a prompting session is not ruled at all. The banner keeps a
// row above the box, so the rule is found among the first rows, not on the first.
func TestYoloFrameRulesTheWholeUI(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.policy = approval.NewPolicy(approval.YOLO, false)
	if !m.yoloFrame() || m.mainWidth() != m.width-2 {
		t.Fatalf("yolo frame not taken: width %d, want %d", m.mainWidth(), m.width-2)
	}
	edges := strings.Split(ansi.Strip(m.View().Content), "\n")
	head := strings.Join(edges[:min(4, len(edges))], "\n")
	last := edges[len(edges)-1]
	if !strings.Contains(head, "\n╭") || !strings.HasPrefix(last, "│") || strings.HasPrefix(last, "╰") {
		t.Fatalf("want a rule on top and sides, none at the bottom: head %q, last %q", head, last)
	}
	// A prompting session gets no box: its last row is the status line, never a
	// rule. (The input box has its own rule, so a per-row scan would find one.)
	manual := strings.Split(ansi.Strip(newTestModelWithHiddenLogs().View().Content), "\n")
	if strings.HasPrefix(manual[len(manual)-1], "╰") {
		t.Fatal("a manual session drew the yolo rule")
	}
}
