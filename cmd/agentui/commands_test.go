package agentui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/turn"
	toolcore "github.com/invokr-ai/invokr/tools/core"
)

type fakeCommands struct {
	command string
	args    []string
}

func (f *fakeCommands) Execute(_ context.Context, command string, args []string) (string, error) {
	f.command, f.args = command, args
	return "result", nil
}

func TestParseCommandAndUnknown(t *testing.T) {
	name, args, ok := parseCommand("/jobs run nightly")
	if !ok || name != "jobs" || strings.Join(args, ",") != "run,nightly" {
		t.Fatalf("parse = %q %v %v", name, args, ok)
	}
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	if _, handled := m.runCommand("/wat"); !handled || m.chatLog.messages[len(m.chatLog.messages)-1].text != "unknown command, try /help" {
		t.Fatalf("unknown command not rejected: %#v", m.chatLog.messages)
	}
}

func TestCommandTableDispatchAndLocalState(t *testing.T) {
	backend := &fakeCommands{}
	m := newModelWithCommands(context.Background(), &fakeSession{}, backend, nil, nil, nil, false, nil, []string{"hello"})
	cmd, handled := m.runCommand("/jobs run nightly")
	if !handled || cmd == nil {
		t.Fatal("backend command not dispatched")
	}
	msg := cmd().(commandResultMsg)
	if msg.err != nil || backend.command != "jobs" || strings.Join(backend.args, ",") != "run,nightly" {
		t.Fatalf("dispatch = %#v %q %v", msg, backend.command, backend.args)
	}
	_, _ = m.runCommand("/progress off")
	if m.chatLog.toolProgress != progressOff {
		t.Fatalf("progress = %v", m.chatLog.toolProgress)
	}
	cmd, _ = m.runCommand("/clear")
	if m.err != ErrNewSession || cmd == nil {
		t.Fatalf("clear did not request a fresh session: %v", m.err)
	}
}

func TestCommandSuggestionsAndHelpShareTable(t *testing.T) {
	suggestions := commandSuggestions()
	// Names and arguments are styled separately, so compare on stripped text.
	help := ansi.Strip(renderCommandHelp(0))
	for _, command := range commandRegistry {
		if !strings.Contains(help, command.usage) {
			t.Fatalf("help missing %s", command.usage)
		}
		if !contains(suggestions, "/"+command.name) {
			t.Fatalf("suggestions missing %s", command.name)
		}
	}
}

type liveCommandSession struct {
	fakeSession
	specs []toolcore.ToolSpec
	usage turn.TokenUsage
}

func (s *liveCommandSession) ToolSpecs() []toolcore.ToolSpec { return s.specs }
func (s *liveCommandSession) Usage() turn.TokenUsage         { return s.usage }

func TestLiveSessionCommands(t *testing.T) {
	session := &liveCommandSession{
		specs: []toolcore.ToolSpec{{Name: "shell", Description: "Run a command. More detail.", Metadata: toolcore.ToolMetadata{AlwaysVisible: true}}},
		usage: turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 100, LatestCompletionTokens: 50, SessionPromptTokens: 700, SessionCompletionTokens: 200},
	}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)

	// /tools and /context render into the popup overlay, not the transcript.
	_, _ = m.runCommand("/tools")
	if got := ansi.Strip(m.overlay.Content()); !strings.Contains(got, "NATIVE TOOLS") || !strings.Contains(got, "Run a command.") {
		t.Fatalf("tools = %q", got)
	}
	session.specs[0].Name = "apply_patch"
	_, _ = m.runCommand("/tools")
	if got := ansi.Strip(m.overlay.Content()); !strings.Contains(got, "apply_patch") {
		t.Fatalf("tools did not read live session state: %q", got)
	}

	_, _ = m.runCommand("/context")
	if got := m.overlay.Content(); !strings.Contains(got, "context: 150 / 1,000 tokens (15%)") || !strings.Contains(got, "session totals: 700 prompt · 200 completion") {
		t.Fatalf("context = %q", got)
	}
	if len(m.chatLog.messages) != 0 {
		t.Fatalf("popup commands must not touch the transcript: %v", m.chatLog.messages)
	}
	session.usage.LatestPromptTokens = 200
	_, _ = m.runCommand("/compact")
	if got := m.chatLog.messages[len(m.chatLog.messages)-1].text; !strings.Contains(got, "context before compaction: context: 250 / 1,000 tokens (25%)") {
		t.Fatalf("compact feedback = %q", got)
	}
}

func TestRenderToolsMarksDeferredSchemas(t *testing.T) {
	got := ansi.Strip(renderTools([]toolcore.ToolSpec{{Name: "mcp_tool", Description: "Deferred tool"}}, 0))
	if !strings.Contains(got, "mcp_tool (deferred)") || !strings.Contains(got, "Deferred tool") {
		t.Fatalf("renderTools() = %q", got)
	}
}

func TestCommandSafetyPolicies(t *testing.T) {
	t.Run("logs unavailable", func(t *testing.T) {
		m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
		_, _ = m.runCommand("/logs")
		if m.logView || !strings.Contains(m.chatLog.messages[len(m.chatLog.messages)-1].text, "unavailable") {
			t.Fatalf("logs state = %v, messages = %#v", m.logView, m.chatLog.messages)
		}
	})

	t.Run("logs open", func(t *testing.T) {
		m := newModel(context.Background(), &fakeSession{}, nil, nil, fakeLogSource{}, true, nil, nil)
		m.width = 80
		_, _ = m.runCommand("/logs")
		if m.logPanel.mode != modeLogs || len(m.chatLog.messages) != 0 {
			t.Fatalf("mode = %v, messages = %#v", m.logPanel.mode, m.chatLog.messages)
		}
	})

	t.Run("jobs while sending", func(t *testing.T) {
		backend := &fakeCommands{}
		m := newModelWithCommands(context.Background(), &fakeSession{}, backend, nil, nil, nil, false, nil, nil)
		m.sending = true
		cmd, _ := m.runCommand("/jobs run nightly")
		if cmd != nil || backend.command != "" || m.chatLog.messages[len(m.chatLog.messages)-1].text != "wait for the current turn to finish" {
			t.Fatalf("job dispatched while sending: cmd=%v backend=%q", cmd, backend.command)
		}
	})

	t.Run("clear with pending approval", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		broker := approval.NewBroker()
		m := newTestModelWithQueue(ctx, broker)
		go func() { _, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell"}) }()
		waitForQueueLen(t, m.approvalQueue, 1)
		m.chatLog.messages = nil
		m.chatLog.Append(message{role: "assistant", text: "keep"})
		_, _ = m.runCommand("/clear")
		if len(m.chatLog.messages) != 2 || !strings.Contains(m.chatLog.messages[1].text, "approvals are pending") {
			t.Fatalf("clear hid pending approval: %#v", m.chatLog.messages)
		}
		m.approvalQueue.Close()
	})
	t.Run("clear during active turn", func(t *testing.T) {
		m := newTestModelWithHiddenLogs()
		m.sending = true
		cmd, _ := m.runCommand("/clear")
		if cmd != nil || m.err != nil || !strings.Contains(m.chatContent(), "turn is running") {
			t.Fatal("clear did not refuse an active turn")
		}
	})
}

func TestPlainKnownCommandsNeverReachModel(t *testing.T) {
	for _, command := range commandRegistry {
		if _, handled := plainCommand(context.Background(), nil, &fakeSession{}, "/"+command.name); !handled {
			t.Errorf("/%s was not handled", command.name)
		}
	}
}

func TestPlainLiveCommands(t *testing.T) {
	session := &liveCommandSession{
		specs: []toolcore.ToolSpec{{Name: "shell", Description: "Run commands."}},
		usage: turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 250},
	}
	tests := map[string]string{
		"/tools":   "NATIVE TOOLS",
		"/context": "context: 250 / 1,000 tokens (25%)",
		"/compact": "context before compaction: context: 250 / 1,000 tokens (25%)",
	}
	for command, want := range tests {
		got, handled := plainCommand(context.Background(), nil, session, command)
		// The tool name is per-rune gradient-painted, so compare on stripped text.
		got = ansi.Strip(got)
		if !handled || !strings.Contains(got, want) {
			t.Errorf("plainCommand(%q) = %q, %v; want %q", command, got, handled, want)
		}
	}
}

func TestCommandRegistryIsComplete(t *testing.T) {
	for _, command := range commandRegistry {
		if command.handler == nil {
			t.Errorf("/%s has no handler", command.name)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// drainSubmit runs the batched send so the fake session records the call, then
// clears the sending flag the way a finished turn does.
func drainSubmit(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("submit command type = %T, want tea.BatchMsg", cmd())
	}
	for _, batched := range batch {
		_ = batched()
	}
	m.sending = false
}

func newRetryModel(session *fakeSession) *model {
	return newModel(context.Background(), session, nil, nil, nil, false, &fakePromptHistory{}, []string{"ls -la /var ."})
}

func TestRetryIgnoresPersistedHistory(t *testing.T) {
	session := &fakeSession{}
	m := newRetryModel(session)
	cmd, handled := m.runCommand("/retry")
	if !handled || cmd != nil {
		t.Fatalf("/retry = %v, handled=%t; want refusal", cmd, handled)
	}
	if len(session.calls) != 0 {
		t.Fatalf("calls = %#v, want none", session.calls)
	}
	if got := m.chatLog.messages[len(m.chatLog.messages)-1].text; !strings.Contains(got, "no prompt sent in this session") {
		t.Fatalf("retry message = %q", got)
	}
}

func TestRetryResendsSessionPrompt(t *testing.T) {
	session := &fakeSession{}
	m := newRetryModel(session)
	m.width = 80
	m.input.SetValue("hello")
	_, cmd := m.submit()
	drainSubmit(t, m, cmd)

	// A slash command between the prompt and /retry must not become the
	// retry target.
	if _, handled := m.runCommand("/help"); !handled {
		t.Fatal("/help was not handled")
	}
	retry, handled := m.runCommand("/retry")
	if !handled || retry == nil {
		t.Fatalf("/retry = %v, handled=%t; want resend", retry, handled)
	}
	drainSubmit(t, m, retry)
	if len(session.calls) != 2 || session.calls[0] != "hello" || session.calls[1] != "hello" {
		t.Fatalf("calls = %#v, want hello twice", session.calls)
	}
}

// A session that reports usage but has not spent a token yet is the issue-355
// case: the window is known, so the pre-guard code printed "context before
// compaction: 0 / N". Compaction must still be requested.
func TestCompactEmptyContextIsQuiet(t *testing.T) {
	session := &liveCommandSession{usage: turn.TokenUsage{Available: true, ContextWindowTokens: 1000}}
	m := newModel(context.Background(), session, nil, nil, nil, false, &fakePromptHistory{}, nil)
	if _, handled := m.runCommand("/compact"); !handled {
		t.Fatal("/compact was not handled")
	}
	if session.compacts != 1 {
		t.Fatalf("compacts = %d, want 1", session.compacts)
	}
	if len(m.chatLog.messages) != 1 || strings.Contains(m.chatLog.messages[0].text, "context before compaction") {
		t.Fatalf("compact output = %#v, want one short line", m.chatLog.messages)
	}
}

// The same quiet single line covers a runtime with no usage source at all.
func TestCompactWithoutUsageSourceIsQuiet(t *testing.T) {
	session := &fakeSession{}
	m := newRetryModel(session)
	if _, handled := m.runCommand("/compact"); !handled {
		t.Fatal("/compact was not handled")
	}
	if session.compacts != 1 {
		t.Fatalf("compacts = %d, want 1", session.compacts)
	}
	if len(m.chatLog.messages) != 1 || strings.Contains(m.chatLog.messages[0].text, "context before compaction") {
		t.Fatalf("compact output = %#v, want one short line", m.chatLog.messages)
	}
}
