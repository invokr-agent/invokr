package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	inference "github.com/rivt-ai/go-inference-router"
)

// Exercise real execution evidence and disk history through two in-memory
// compactions and a fresh session. Summaries are fake; this tests no model quality.
func TestRepeatedCompactionThenDiskResume(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := sessionstore.New(dir)
	request := "Write allowed.txt; do not change secret.txt."
	runner := testRunner(t, nil, []string{"", `{"type":"reply","message":"Work recorded"}`})
	caller := runner.turnProcessor.tools.(*tools.Caller)
	caller.RegisterNative(core.ToolSpec{Name: "demo", Metadata: core.ToolMetadata{AlwaysVisible: true, Capabilities: core.ToolCapabilities{WritesWorkspace: true}}}, func(ctx context.Context, call *core.ToolCall) (string, error) {
		if strings.Contains(string(call.Arguments), "secret") {
			return "", core.EnforceApproval(ctx, testutil.StaticApprover{Decision: approval.Decision{Approved: false}}, core.ToolSpec{Name: "demo"}, core.Ask(approval.Request{Tool: "demo"}))
		}
		return "wrote allowed.txt", os.WriteFile(filepath.Join(dir, "allowed.txt"), []byte("changed"), 0600)
	})
	gateway := runner.modelGateway.(*sequenceGateway)
	gateway.turns[0] = ModelTurn{Messages: []inference.Message{inference.SystemMessage("system"), inference.UserMessage(request)}, Completion: completionOutput{toolCalls: []inference.ToolCall{
		{ID: "write", Name: "demo", Arguments: `{"path":"allowed.txt"}`},
		{ID: "deny", Name: "demo", Arguments: `{"path":"secret.txt"}`},
	}}}
	runner.events.(*events.OrderedDispatcher).Register(events.HandlerFunc(func(ctx context.Context, event events.Event) error {
		if event.Type == events.EventAfterResponse && event.Response != nil {
			r := event.Response
			_, err := store.AppendTurn(ctx, "s1", "assistant", r.Text, sessionstore.ToolRecord{Evidence: r.ExecutionEvidence, Calls: r.ToolOutcomes, Count: r.ToolCallCount})
			if err != nil {
				t.Error(err)
			}
			return err
		}
		return nil
	}))
	if _, err := store.Append(ctx, "s1", "user", request); err != nil {
		t.Fatal(err)
	}
	response, err := runner.Run(ctx, RunRequest{SessionID: "s1", Message: request})
	if err != nil || response.ToolCallCount != 2 {
		t.Fatalf("run: response=%+v err=%v", response, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "allowed.txt")); err != nil || string(data) != "changed" {
		t.Fatalf("write missing: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write ran: %v", err)
	}
	summaries := &compactionFlowGateway{summaries: []string{"first summary", "second summary"}}
	budget := &contextBudget{gateway: summaries, writesWorkspace: func(name string) bool { return name == "demo" }}
	for cycle := range 2 {
		for _, message := range []inference.Message{inference.UserMessage("continue"), inference.AssistantMessage("inspected"), inference.UserMessage("check again"), inference.AssistantMessage("checked")} {
			runner.messages = append(runner.messages, message)
			if _, err := store.Append(ctx, "s1", string(message.Role), message.Content); err != nil {
				t.Fatal(err)
			}
		}
		before, err := store.Load(ctx, "s1")
		if err != nil {
			t.Fatal(err)
		}
		runner.messages, err = budget.summarizeMessages(ctx, runner.messages, planRetention(runner.messages, 0, 1, budget.writesWorkspace))
		if err != nil {
			t.Fatal(err)
		}
		assertCompactedExecutionPair(t, runner.messages, "write", "wrote allowed.txt")
		assertCompactedExecutionPair(t, runner.messages, "deny", "denied")
		if runner.messages[1].Content != request || runner.messages[2].Role != inference.RoleAssistant || !strings.Contains(runner.messages[2].Content, summaries.summaries[cycle]) {
			t.Fatalf("cycle %d lost request/summary: %v", cycle, runner.messages)
		}
		after, err := store.Load(ctx, "s1")
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("compaction changed disk history: %v", err)
		}
	}
	if summaries.summaryCalls != 2 {
		t.Fatalf("summary calls=%d", summaries.summaryCalls)
	}
	assertCompactionDiskResume(t, dir, request)
}

func assertCompactedExecutionPair(t *testing.T, messages []inference.Message, id, content string) {
	t.Helper()
	if orphaned := orphanedToolCallIDs(messages); len(orphaned) != 0 {
		t.Fatalf("orphaned calls: %v", orphaned)
	}
	for _, message := range messages {
		if message.ToolCallID == id && strings.Contains(message.Content, content) {
			return
		}
	}
	t.Fatalf("lost %s result containing %q", id, content)
}

func assertCompactionDiskResume(t *testing.T, dir, request string) {
	t.Helper()
	ctx := context.Background()
	loaded, err := sessionstore.New(dir).Load(ctx, "s1")
	if err != nil || len(loaded) != 10 {
		t.Fatalf("disk history: len=%d err=%v", len(loaded), err)
	}
	if loaded[0].Text != request || loaded[1].Text != "Work recorded" || len(loaded[1].ExecutionEvidence) != 2 {
		t.Fatalf("original history/evidence lost: %+v", loaded[:2])
	}
	outcomes := map[string]string{}
	for _, evidence := range loaded[1].ExecutionEvidence {
		outcomes[evidence.CallID] = evidence.Outcome
	}
	if outcomes["write"] != "completed" || outcomes["deny"] != "failed" {
		t.Fatalf("wrong evidence: %v", outcomes)
	}
	if len(loaded[1].ToolCalls) != 1 || loaded[1].ToolCalls[0].Kind != turn.OutcomeKindDenied || loaded[1].ExecutionEvidence[1].Code != "denied" {
		t.Fatalf("lost denial classification: %+v", loaded[1])
	}
	transcript := make([]turn.TranscriptMessage, len(loaded))
	for i, msg := range loaded {
		transcript[i] = turn.TranscriptMessage{Role: msg.Role, Text: msg.Text, ToolCalls: msg.ToolCalls, ToolCallCount: msg.ToolCallCount, ExecutionEvidence: msg.ExecutionEvidence}
	}
	session := &Session{sessionID: "s1", runner: testRunner(t, nil, []string{`{"type":"reply","message":"resumed"}`})}
	gateway := &historyGateway{replies: []string{"resumed"}}
	session.runner.modelGateway = gateway
	if err := session.ResumeTranscript(transcript); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Send(ctx, "continue after restart"); err != nil {
		t.Fatal(err)
	}
	sent := gateway.beginCalls[0].History
	for _, want := range []string{request, "Work recorded", "historical", "completed:1", "failed:1", "denied"} {
		if !requestsContainMessage([][]inference.Message{sent}, want) {
			t.Fatalf("resumed request lost %q: %+v", want, sent)
		}
	}
}
