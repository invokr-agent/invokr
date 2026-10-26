package agentui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/surface/ui"
)

// TestUXGallery renders every UX variant to UX_GALLERY/<name>.ans for review:
//
//	UX_GALLERY=$PWD/docs/assets/record/gallery go test ./cmd/agentui -run TestUXGallery -count=1
//
// then docs/assets/record/snap.py turns them into PNGs.
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

	tool := func(kind events.EventType, id, name, args, out, code string) events.Event {
		return events.Event{Type: kind, Tool: &events.ToolPayload{ID: id, Name: name, Arguments: args, Output: out, Code: code}}
	}
	conversation := []events.Event{
		{Type: events.EventReasoning, Reasoning: &events.ReasoningPayload{Text: "The off-by-one is in `last()`.\n\nCheck the tests first, then fix the index.\n\nRun them again after."}},
		tool(events.EventToolStarted, "1", "bash", `{"command":"go test ./..."}`, "", ""),
		tool(events.EventToolCompleted, "1", "bash", `{"command":"go test ./..."}`, "--- FAIL: TestLast\n    want 3, got 2\nFAIL", "exit 1"),
		tool(events.EventToolStarted, "2", "edit", `{"path":"todo.go","edits":[{"oldText":"return items[len(items)-2]","newText":"return items[len(items)-1]"}]}`, "", ""),
		tool(events.EventToolCompleted, "2", "edit", `{"path":"todo.go","edits":[{"oldText":"return items[len(items)-2]","newText":"return items[len(items)-1]"}]}`, "ok", ""),
		tool(events.EventToolStarted, "3", "web_search", `{"query":"x"}`, "", ""),
		tool(events.EventToolFailed, "3", "web_search", `{"query":"x"}`, "", ""),
		{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: "Fixed `last()`: it read the **second-to-last** item.\n\n```go\nreturn items[len(items)-1]\n```"}},
	}
	scenes := []struct {
		name  string
		setup func(m *model)
	}{
		{"conversation", func(m *model) {}},
		{"notices", func(m *model) {
			m.chatLog.Append(message{role: "tool", text: "mode: auto (future calls; running work keeps its permissions)"})
			m.chatLog.Append(message{role: "tool", text: "approval queue cleared"})
		}},
		{"yolo", func(m *model) { m.policy = approval.NewPolicy(approval.YOLO, false) }},
		{"thinking-expanded", func(m *model) { m.chatLog.messages[0].expanded = true }},
	}
	for _, dark := range []bool{true, false} {
		ui.Invokr = ui.NewTheme(dark)
		theme := map[bool]string{true: "dark", false: "light"}[dark]
		for _, width := range []int{80, 120} {
			for _, s := range scenes {
				m := newTestModelWithHiddenLogs()
				m.width, m.height = width, 40
				m.applyTheme()
				for _, ev := range conversation {
					m.chatLog.ApplyEvent(ev)
				}
				s.setup(m)
				write(t, dir, fmt.Sprintf("%s-%s-%d", s.name, theme, width), m.View().Content)
			}
			write(t, dir, fmt.Sprintf("help-%s-%d", theme, width), renderCommandHelp(width))
		}
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".ans"), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
