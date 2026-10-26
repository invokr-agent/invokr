package agentui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
)

const editedArgs = `{"path":"README.md","edits":[{"oldText":"alpha","newText":"omega"}]}`

func TestEditedFileDiff(t *testing.T) {
	for _, tc := range []struct{ name, tool, args, want string }{
		{name: "edits", tool: "edit", args: editedArgs,
			want: "*** Begin Patch\n*** Update File: README.md\n@@\n-alpha\n+omega\n*** End Patch"},
		{name: "full write", tool: "edit", args: `{"path":"a.md","content":"one\ntwo\n"}`,
			want: "*** Begin Patch\n*** Add File: a.md\n@@\n+one\n+two\n*** End Patch"},
		{name: "crlf edits", tool: "edit", args: `{"path":"a.md","edits":[{"oldText":"one\r\n","newText":"two\r\n"}]}`,
			want: "*** Begin Patch\n*** Update File: a.md\n@@\n-one\n+two\n*** End Patch"},
		{name: "crlf write", tool: "edit", args: `{"path":"a.md","content":"one\r\ntwo\r\n"}`,
			want: "*** Begin Patch\n*** Add File: a.md\n@@\n+one\n+two\n*** End Patch"},
		{name: "removal", tool: "edit", args: `{"path":"a.md","edits":[{"oldText":"gone\n","newText":""}]}`,
			want: "*** Begin Patch\n*** Update File: a.md\n@@\n-gone\n*** End Patch"},
		{name: "dry run", tool: "edit", args: `{"path":"a.md","dry_run":true,"edits":[{"oldText":"a","newText":"b"}]}`},
		{name: "delete", tool: "edit", args: `{"path":"a.md","delete":true}`},
		{name: "bad json", tool: "edit", args: `{`},
		{name: "no path", tool: "edit", args: `{"edits":[{"oldText":"a","newText":"b"}]}`},
		{name: "empty write", tool: "edit", args: `{"path":"a.md","content":""}`},
		{name: "other tool", tool: "apply_patch", args: `{"patch":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := editedFileDiff(tc.tool, tc.args); got != tc.want {
				t.Fatalf("editedFileDiff = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEditRendererSelection(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want blockRenderer
	}{
		{tool: "edit", want: patchRenderer{}},
		{tool: "web_search", want: genericRenderer{}},
	} {
		if got := rendererFor(tc.tool); got != tc.want {
			t.Fatalf("rendererFor(%q) = %T, want %T", tc.tool, got, tc.want)
		}
	}
}

func TestEditRendererGolden(t *testing.T) {
	m := message{role: "tool", toolName: "edit", toolStatus: toolDone, toolDiff: editedWant}
	got := ansi.Strip(renderSettledBlock(m, 60))
	for _, want := range []string{"README.md", "alpha", "omega"} {
		if !strings.Contains(got, want) {
			t.Fatalf("edit summary missing %q:\n%s", want, got)
		}
	}
}

const editedWant = "*** Begin Patch\n*** Update File: README.md\n@@\n-alpha\n+omega\n*** End Patch"

func TestChatLogRendersEditedFileDiff(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  string
		wants bool
	}{
		{name: "applied", wants: true},
		{name: "flagged", code: "denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := newChatLog(progressAll)
			payload := &events.ToolPayload{ID: "t1", Name: "edit", Arguments: editedArgs}
			log.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: payload, Timestamp: time.Now()})
			done := *payload
			done.Code = tc.code
			log.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &done})
			content := ansi.Strip(log.Content(80, "*"))
			if has := strings.Contains(content, "omega"); has != tc.wants {
				t.Fatalf("diff shown = %v, want %v\n%s", has, tc.wants, content)
			}
		})
	}
}

// TestApprovedWriteKeepsItsDiff: a whole-file write's settled block shows the
// before/after that was approved, not the new file as if it were added.
func TestApprovedWriteKeepsItsDiff(t *testing.T) {
	var c chatLog
	args := `{"path":"README.md","content":"# Todo\n"}`
	c.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &events.ToolPayload{ID: "1", Name: "edit", Arguments: args}})
	c.insertAboveRunning("edit", message{role: "tool", text: "approval granted: edit"}, &approval.FileDiff{Path: "README.md", Before: "# todo\n", After: "# Todo\n"})
	c.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &events.ToolPayload{ID: "1", Name: "edit", Arguments: args, Output: "ok"}})
	var b strings.Builder
	writeMessage(&b, *c.Last(), 80, "")
	if got := ansi.Strip(b.String()); !strings.Contains(got, "+1 −1") {
		t.Fatalf("settled write = %q, want the approved +1 −1", got)
	}
}
