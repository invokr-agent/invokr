package agentui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-agent/invokr/platform/events"
)

const testPatch = "*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n*** End Patch"

func TestAppliedPatchDiff(t *testing.T) {
	for _, tc := range []struct{ name, tool, args, want string }{
		{name: "apply_patch", tool: "apply_patch", args: `{"patch":"` + strings.ReplaceAll(testPatch, "\n", `\n`) + `"}`, want: testPatch},
		{name: "dry run", tool: "apply_patch", args: `{"patch":"x","dry_run":true}`},
		{name: "other tool", tool: "shell", args: `{"patch":"x"}`},
		{name: "bad json", tool: "apply_patch", args: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := appliedPatchDiff(tc.tool, tc.args); got != tc.want {
				t.Fatalf("appliedPatchDiff = %q, want %q", got, tc.want)
			}
		})
	}
}

// A completed apply_patch must show its diff inline; a flagged one must not,
// because the patch may not have applied.
func TestChatLogRendersAppliedPatchDiff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    string
		wantHas bool
	}{
		{name: "applied", wantHas: true},
		{name: "flagged", code: "denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := newChatLog(progressAll)
			args := `{"patch":"` + strings.ReplaceAll(testPatch, "\n", `\n`) + `"}`
			payload := &events.ToolPayload{ID: "t1", Name: "apply_patch", Arguments: args}
			log.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: payload, Timestamp: time.Now()})
			done := *payload
			done.Code = tc.code
			log.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &done})

			content := ansi.Strip(log.Content(80, "*"))
			// DiffView lays the row out as "<gutter> + <content>".
			if has := strings.Contains(content, "+ new"); has != tc.wantHas {
				t.Fatalf("diff shown = %v, want %v\n%s", has, tc.wantHas, content)
			}
		})
	}
}

func TestRenderDiffBlockTruncates(t *testing.T) {
	got := renderDiffBlock(strings.TrimSuffix(strings.Repeat("+line\n", maxDiffLines+5), "\n"), 80, maxDiffLines)
	if !strings.Contains(got, "5 more lines") {
		t.Fatalf("no truncation notice: %s", got)
	}
}
