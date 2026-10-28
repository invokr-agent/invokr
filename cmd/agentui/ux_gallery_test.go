package agentui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/surface/ui"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/tool_search"
)

// TestUXGallery renders every UX variant to UX_GALLERY/<name>.ans for review:
//
//	make screenshots
//
// or UX_GALLERY=<dir> go test ./cmd/agentui -run TestUXGallery -count=1, then
// docs/assets/record/snap.py turns them into PNGs.
//
// Skipped without UX_GALLERY. Add a scene to cover a new variant.
func TestUXGallery(t *testing.T) {
	dir := os.Getenv("UX_GALLERY")
	if dir == "" {
		t.Skip("set UX_GALLERY=<dir> to render the gallery")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer func() { ui.Invokr = ui.NewTheme(true) }()

	for _, dark := range []bool{true, false} {
		ui.Invokr = ui.NewTheme(dark)
		theme := map[bool]string{true: "dark", false: "light"}[dark]
		for _, width := range []int{80, 120} {
			for i, s := range galleryScenes {
				g := newGallery(t, width)
				// Every scene but the first runs on top of a short history, so it
				// reads in context rather than on an empty screen.
				if s.name != "empty" {
					g.m.chatLog.Append(message{role: "user", text: "the todo tests fail, fix last()"})
					g.events(conversation[:5]...)
				}
				s.run(g)
				write(t, dir, fmt.Sprintf("%02d-%s-%s-%d", i+1, s.name, theme, width), g.m.View().Content)
				g.close()
			}
			write(t, dir, fmt.Sprintf("%02d-help-text-%s-%d", len(galleryScenes)+1, theme, width), renderCommandHelp(width))
		}
	}
}

type gallery struct {
	t      *testing.T
	m      *model
	broker *approval.Broker
	queue  *approval.ApprovalQueue
	cancel context.CancelFunc
	done   chan struct{}
	asked  int
}

func newGallery(t *testing.T, width int) *gallery {
	ctx, cancel := context.WithCancel(context.Background())
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)
	session := &dialogSession{
		liveCommandSession: liveCommandSession{specs: []core.ToolSpec{{Name: "read", Description: "Read files."}, {Name: "web_search", Description: "Search the web."}, {Name: "shell", Description: "Run shell commands."}}},
		listings:           []tool_search.Listing{{Name: "read", Enabled: true, Protected: true}, {Name: "shell", Enabled: true}, {Name: "web_search"}},
	}
	var lines []string
	for i := range 24 {
		lines = append(lines, fmt.Sprintf("12:01:%02d %s", i, []string{"INFO tool shell exit=0", "INFO model reply tokens=412", "WARN slow model response 8.2s", "ERROR web_search: 503 from upstream"}[i%4]))
	}
	logs := fakeLogSource{lines: lines}
	m := newModel(ctx, session, nil, queue, logs, true, &fakePromptHistory{}, nil)
	m.width, m.height = width, 40
	m.applyTheme()
	return &gallery{t: t, m: m, broker: broker, queue: queue, cancel: cancel, done: make(chan struct{}, 16)}
}

func (g *gallery) close() {
	g.queue.DecideAll(approval.Decision{Reason: "gallery cleanup"})
	for range g.asked {
		<-g.done
	}
	g.queue.Close()
	g.cancel()
}

// ask raises approval requests the way a tool would and waits until they queue.
func (g *gallery) ask(reqs ...approval.Request) {
	for _, req := range reqs {
		g.asked++
		go func() {
			_, _ = g.broker.RequestApproval(context.Background(), req)
			g.done <- struct{}{}
		}()
		waitForQueueLen(g.t, g.queue, g.queue.Len()+1)
	}
}

func (g *gallery) keys(keys ...tea.KeyPressMsg) {
	for _, k := range keys {
		g.m.updateKey(k) //nolint:errcheck
	}
}

func (g *gallery) events(evs ...events.Event) {
	for _, ev := range evs {
		g.m.chatLog.ApplyEvent(ev)
	}
}

func (g *gallery) command(text string) { g.m.runCommand(text) } //nolint:errcheck

func toolEvent(kind events.EventType, id, name, args, out, code string) events.Event {
	return events.Event{Type: kind, Tool: &events.ToolPayload{ID: id, Name: name, Arguments: args, Output: out, Code: code}}
}

func reasoning(text string) events.Event {
	return events.Event{Type: events.EventReasoning, Reasoning: &events.ReasoningPayload{Text: text}}
}

func answer(text string) events.Event {
	return events.Event{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: text}}
}

const (
	editArgs  = `{"path":"todo.go","edits":[{"oldText":"return items[len(items)-2]","newText":"return items[len(items)-1]"}]}`
	writeArgs = `{"path":"notes.md","content":"# Notes\n\n- fix last()\n- add a test\n"}`
)

var (
	shellReq = approval.Request{Tool: "shell", Tier: "code_execution", Summary: "Run a shell command", Subject: "go test ./... | tail -20"}
	chainReq = approval.Request{Tool: "shell", Tier: "code_execution", Summary: "Run a shell command", Subject: "git add -A && git commit -m 'fix last' && git push origin dev"}
	editReq  = approval.Request{Tool: "edit", Tier: "write", Summary: "Edit todo.go", Subject: "todo.go",
		Diff: &approval.FileDiff{Path: "todo.go", Before: "package todo\n\nfunc last(items []string) string {\n\treturn items[len(items)-2]\n}\n",
			After: "package todo\n\nfunc last(items []string) string {\n\treturn items[len(items)-1]\n}\n"}}
	netReq     = approval.Request{Tool: "web_fetch", Tier: "network", Summary: "Fetch a web page", Subject: "https://go.dev/doc", Network: true}
	outsideReq = approval.Request{Tool: "read", Tier: "read", Summary: "Read outside the workspace", Subject: "/etc/hosts", Outside: true}

	conversation = []events.Event{
		reasoning("The off-by-one is in `last()`.\n\nCheck the tests first, then fix the index.\n\nRun them again after."),
		toolEvent(events.EventToolStarted, "1", "bash", `{"command":"go test ./..."}`, "", ""),
		toolEvent(events.EventToolCompleted, "1", "bash", `{"command":"go test ./..."}`, "--- FAIL: TestLast\n    want 3, got 2\nFAIL", "exit 1"),
		toolEvent(events.EventToolStarted, "2", "edit", editArgs, "", ""),
		toolEvent(events.EventToolCompleted, "2", "edit", editArgs, "ok", ""),
		toolEvent(events.EventToolStarted, "3", "edit", writeArgs, "", ""),
		toolEvent(events.EventToolCompleted, "3", "edit", writeArgs, "ok", ""),
		toolEvent(events.EventToolStarted, "4", "read", `{"path":"todo_test.go"}`, "", ""),
		toolEvent(events.EventToolCompleted, "4", "read", `{"path":"todo_test.go"}`, "package todo\n\nfunc TestLast(t *testing.T) {}\n", ""),
		toolEvent(events.EventToolStarted, "5", "web_search", `{"query":"go slices last element"}`, "", ""),
		toolEvent(events.EventToolFailed, "5", "web_search", `{"query":"go slices last element"}`, "", ""),
		toolEvent(events.EventToolStarted, "6", "bash", `{"command":"rm -rf build"}`, "", ""),
		toolEvent(events.EventToolCompleted, "6", "bash", `{"command":"rm -rf build"}`, "denied by user", "denied"),
		answer("Fixed `last()`: it read the **second-to-last** item.\n\n```go\nreturn items[len(items)-1]\n```\n\n| file | change |\n|---|---|\n| todo.go | index |"),
	}
)

var galleryScenes = []struct {
	name string
	run  func(g *gallery)
}{
	// Startup and input.
	{"empty", func(g *gallery) {}},
	{"draft-multiline", func(g *gallery) {
		g.m.input.SetValue("Fix the off-by-one in last()\nand add a regression test\nthen run go test")
	}},
	{"slash-suggestions", func(g *gallery) { g.keys(testTextKey("/")) }},
	{"slash-suggestions-filtered", func(g *gallery) { g.keys(testTextKey("/"), testTextKey("s")) }},
	{"command-palette", func(g *gallery) { g.keys(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}) }},
	{"key-hints-toggled", func(g *gallery) { g.keys(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}) }},

	// Transcript.
	{"conversation", func(g *gallery) { g.events(conversation...) }},
	{"thinking-expanded", func(g *gallery) {
		g.events(conversation...)
		g.m.chatLog.messages[0].expanded = true
	}},
	{"tool-running", func(g *gallery) {
		g.m.sending = true
		g.events(reasoning("Run the tests."), toolEvent(events.EventToolStarted, "1", "bash", `{"command":"go test ./..."}`, "", ""),
			events.Event{Type: events.EventToolProgress, Tool: &events.ToolPayload{ID: "1", Name: "bash", Progress: "ok  \tgithub.com/x/todo\t0.4s"}})
	}},
	{"answer-streaming", func(g *gallery) {
		g.m.sending = true
		g.events(answer("Looking at `last()` now — the index is"))
	}},
	{"browse-details", func(g *gallery) {
		g.events(conversation...)
		g.keys(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	}},
	{"browse-details-expanded", func(g *gallery) {
		g.events(conversation...)
		g.keys(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}, testKey(tea.KeyUp), testKey(tea.KeyEnter))
	}},
	{"notices", func(g *gallery) {
		g.events(conversation[:3]...)
		g.keys(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		g.m.chatLog.Append(message{role: "tool", text: "approval queue cleared"})
	}},
	{"queued-messages", func(g *gallery) {
		g.m.sending = true
		g.events(conversation[:2]...)
		for _, text := range []string{"also check the README", "and bump the version"} {
			g.m.input.SetValue(text)
			g.keys(testKey(tea.KeyEnter))
		}
	}},
	{"resumed-session", func(g *gallery) {
		g.m.appendSavedTranscript(turn.TranscriptMessage{Role: "user", Text: "fix the failing test"})
		g.m.appendSavedTranscript(turn.TranscriptMessage{Role: "assistant", Text: "Fixed `last()`; tests pass.", ExecutionEvidence: []turn.ExecutionEvidence{
			{CallID: "a", Tool: "shell", Command: "go test ./...", Outcome: "failed", Code: "exit 1"},
			{CallID: "b", Tool: "edit", Outcome: "completed"},
			{CallID: "c", Tool: "shell", Command: "go test ./...", Outcome: "completed"},
		}})
		g.m.appendSavedTranscript(turn.TranscriptMessage{Role: "user", Text: "now push it"})
		g.m.appendSavedTranscript(turn.TranscriptMessage{Role: "assistant", Interrupted: true})
	}},

	// Approvals.
	{"approval-shell", func(g *gallery) { g.events(conversation[:2]...); g.ask(shellReq) }},
	{"approval-chained", func(g *gallery) { g.ask(chainReq) }},
	{"approval-edit-diff", func(g *gallery) { g.ask(editReq) }},
	{"approval-edit-details", func(g *gallery) { g.ask(editReq); g.keys(testTextKey("v")) }},
	{"approval-network", func(g *gallery) { g.ask(netReq) }},
	{"approval-outside", func(g *gallery) { g.ask(outsideReq) }},
	{"approval-queue", func(g *gallery) { g.ask(shellReq, editReq, netReq) }},
	{"approval-queue-next", func(g *gallery) { g.ask(shellReq, editReq, netReq); g.keys(testTextKey("j")) }},
	{"approval-yes", func(g *gallery) { g.answered(shellReq, "y") }},
	{"approval-no", func(g *gallery) { g.answered(shellReq, "n") }},
	{"approval-always", func(g *gallery) { g.answered(shellReq, "a") }},
	{"approval-yes-all", func(g *gallery) { g.answered(shellReq, "Y", editReq, netReq) }},
	{"approval-no-all", func(g *gallery) { g.answered(shellReq, "N", editReq, netReq) }},
	{"approval-hidden-behind-logs", func(g *gallery) { g.m.showLogs(); g.ask(shellReq) }},

	// Modes and overlays.
	{"yolo", func(g *gallery) {
		g.m.policy = approval.NewPolicy(approval.YOLO, false)
		g.events(conversation...)
	}},
	{"auto-mode", func(g *gallery) { g.m.policy = approval.NewPolicy(approval.Auto, false) }},
	{"logs", func(g *gallery) { g.m.showLogs() }},
	{"help-overlay", func(g *gallery) { g.command("/help") }},
	{"settings", func(g *gallery) { g.command("/settings") }},
	{"usage", func(g *gallery) { g.command("/usage") }},
	{"tools", func(g *gallery) { g.command("/tools") }},
	{"recorded-calls", func(g *gallery) { g.events(conversation...); g.command("/recorded") }},
	{"clear-refused-pending", func(g *gallery) {
		g.ask(shellReq)
		g.m.input.SetValue("/clear")
		g.keys(testKey(tea.KeyEnter))
	}},
}

func (g *gallery) answered(first approval.Request, key string, more ...approval.Request) {
	g.events(toolEvent(events.EventToolStarted, "1", first.Tool, `{"command":"`+strings.ReplaceAll(first.Subject, `"`, `\"`)+`"}`, "", ""))
	g.ask(append([]approval.Request{first}, more...)...)
	g.keys(testTextKey(key))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".ans"), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
