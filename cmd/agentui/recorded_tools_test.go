package agentui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

// IDs, names, arguments, final prose, and evidence are from the current-dev
// #665 request-2 capture in audit-inbox/665-current-dev.
func TestRecordedToolsExposeCapturedEditDespiteFinalCreateLabel(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	editID := "7VBlrE7HNOH7IlDyNbVlFRlPSe7jLrsO"
	readID := "GWe5Qpwdc4jaxkKLSBLKbWbIfeJS3K92"
	for _, tool := range []events.ToolPayload{
		{ID: editID, Name: "edit", Arguments: `{"path":"qa-bot4-manual/queue-b.txt","content":"B"}`},
		{ID: readID, Name: "read", Arguments: `{"paths":["qa-bot4-manual/queue-a.txt", "qa-bot4-manual/queue-b.txt"]}`},
	} {
		m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &tool})
		m.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &tool})
	}
	final := "Both succeeded.\n- **create** → `qa-bot4-manual/queue-b.txt`\n- **read** → both files"
	m.updateResponse(responseMsg{text: final, evidence: []turn.ExecutionEvidence{
		{CallID: editID, Tool: "edit", Outcome: "completed", Started: 1, Finished: 2},
		{CallID: readID, Tool: "read", Outcome: "completed", ReadOnly: true, Started: 3, Finished: 4},
	}})
	history := ansi.Strip(m.recorded)
	for _, want := range []string{"Execution history", "edit \"qa-bot4-manual/queue-b.txt\"", "read \"qa-bot4-manual/queue-a.txt, qa-bot4-manual/queue-b.txt\"", "completed"} {
		if !strings.Contains(history, want) {
			t.Fatalf("recorded history lacks %q: %s", want, history)
		}
	}
	if shown := ansi.Strip(m.chatContent()); !strings.Contains(shown, "**create**") && !strings.Contains(shown, "create") {
		t.Fatalf("model prose changed: %s", shown)
	}
}

func TestRecordedToolsFallbackWithoutEventArgs(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	tool := events.ToolPayload{ID: "missing", Name: "edit"}
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &tool})
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &tool})
	m.updateResponse(responseMsg{text: "done", evidence: []turn.ExecutionEvidence{{CallID: "missing", Tool: "edit", Outcome: "completed", Finished: 2}}})
	history := ansi.Strip(m.recorded)
	if !strings.Contains(history, "edit \"arguments unavailable\": completed") || strings.Contains(history, "queue-b.txt") {
		t.Fatalf("fallback invented a path: %s", history)
	}
}

func TestRecordedToolsBoundManyCalls(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	evidence := make([]turn.ExecutionEvidence, 6)
	for i := range evidence {
		id := fmt.Sprintf("call-%d", i)
		tool := events.ToolPayload{ID: id, Name: "read", Arguments: fmt.Sprintf(`{"path":"file-%d.txt"}`, i)}
		m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &tool})
		m.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &tool})
		evidence[i] = turn.ExecutionEvidence{CallID: id, Tool: "read", Outcome: "completed", Started: 2*i + 1, Finished: 2*i + 2}
	}
	m.updateResponse(responseMsg{text: "done", evidence: evidence})
	history := ansi.Strip(m.recorded)
	for _, want := range []string{"file-0.txt", "file-3.txt", "file-4.txt", "file-5.txt"} {
		if !strings.Contains(history, want) {
			t.Fatalf("bounded history lacks %q: %s", want, history)
		}
	}
	if details := history; !strings.Contains(details, "file-0.txt") {
		t.Fatalf("expandable full history lost earlier call: %s", details)
	}
}

func TestRecordedToolsLongPathsShowOmissionAndKeepRawArgs(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	paths := make([]string, 12)
	for i := range paths {
		paths[i] = fmt.Sprintf("very-long-workspace-folder-%02d/read-result-%02d.txt", i, i)
	}
	args := fmt.Sprintf(`{"paths":["%s"]}`, strings.Join(paths, `","`))
	tool := events.ToolPayload{ID: "read-long", Name: "read", Arguments: args}
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &tool})
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &tool})
	m.updateResponse(responseMsg{text: "done", evidence: []turn.ExecutionEvidence{{CallID: tool.ID, Tool: "read", Outcome: "completed", Finished: 2}}})
	history := ansi.Strip(m.recorded)
	if !strings.Contains(history, "read \"...") {
		t.Fatalf("long paths lack visible bounded elision: %s", history)
	}
	if m.chatLog.recordedTools[0].toolArgs != args {
		t.Fatal("recorded preview changed raw tool arguments")
	}
	if got := primaryArg("read", args); len([]rune(got)) > toolPreviewMax {
		t.Fatalf("tool chip preview widened: %s", got)
	}
}

func TestRecordedToolsWithReusedCallIDs(t *testing.T) {
	for _, mode := range []progressMode{progressAll, progressNew, progressOff} {
		for _, reset := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/reset=%v", mode, reset), func(t *testing.T) {
				log := newChatLog(mode)
				for i, path := range []string{"old.txt", "current.txt"} {
					if i > 0 && reset {
						log.recordedTools = nil
					}
					start := log.nextID
					tool := events.ToolPayload{ID: "reused", Name: "read", Arguments: fmt.Sprintf(`{"path":%q}`, path)}
					log.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &tool})
					tool.Arguments = ""
					log.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &tool})
					evidence := log.observedEvidence(start)
					if len(evidence) != 1 || evidence[0].Outcome != "completed" {
						t.Fatalf("request evidence: %+v", evidence)
					}
					history := log.executionHistory(start, nil, evidence)
					if !strings.Contains(history, path) || (i > 0 && strings.Contains(history, "old.txt")) {
						t.Fatalf("stale arguments: %s", history)
					}
					for _, msg := range log.messages {
						if msg.toolStatus == toolRunning {
							t.Fatal("completed call still running")
						}
					}
				}
				want := 2
				if reset {
					want = 1
				}
				if len(log.recordedTools) != want {
					t.Fatalf("recorded calls=%d want=%d", len(log.recordedTools), want)
				}
			})
		}
	}
}
