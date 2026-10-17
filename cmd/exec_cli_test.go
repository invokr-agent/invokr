package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/cmd/execwire"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/platform/turn"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type execFakeSession struct {
	id          string
	message     string
	seen        string
	failures    []turn.ToolOutcome
	callCount   int
	usage       turn.TokenUsage
	resumed     []turn.TranscriptMessage
	model       string
	servedModel string
}

// resumedText flattens the replayed transcript for assertions.
func (s *execFakeSession) resumedText() string {
	parts := make([]string, 0, len(s.resumed))
	for _, item := range s.resumed {
		parts = append(parts, item.Role+": "+item.Text)
	}
	return strings.Join(parts, "\n")
}

// execResumeServices wires the fake as the session a resume reopens by ID.
func execResumeServices(app config.AppConfig, session *execFakeSession) runtime.Services {
	return runtime.Services{
		App: app, Events: events.NewOrderedDispatcher(nil), Approvals: approval.NewBroker(), Session: session,
		NewChatSession: func(string) (turn.IdentifiedSession, *approval.Broker, error) {
			return session, approval.NewBroker(), nil
		},
	}
}

// ResumeTranscript is what makes the fake resumable: exec continues the stored
// session itself now, instead of pasting a transcript summary into the prompt.
func (s *execFakeSession) ResumeTranscript(transcript []turn.TranscriptMessage) error {
	s.resumed = transcript
	return nil
}

func (s *execFakeSession) Send(_ context.Context, message string) (turn.AgentResponse, error) {
	s.seen = message
	return turn.AgentResponse{Message: s.message, Model: s.model, ServedModel: s.servedModel, ToolFailures: s.failures, ToolCallCount: s.callCount, Usage: s.usage}, nil
}

// The ledger must name the model the run actually used, not whatever config
// held at startup — a router profile pin or a /model switch can differ.
func TestRunExecPromptReportsRequestedAndServedModel(t *testing.T) {
	dir := t.TempDir()
	session := &execFakeSession{id: "s1", message: "done", model: "asked-model", servedModel: "served-model"}
	app := config.AppConfig{Memory: config.MemoryConfig{Dir: dir}, Provider: config.ProviderConfig{Model: "config-model"}}.WithDefaults()
	services := runtime.Services{App: app, Events: events.NewOrderedDispatcher(nil), Approvals: approval.NewBroker(), Session: session}
	var out bytes.Buffer
	if err := runExecPrompt(context.Background(), services, &out, &bytes.Buffer{}, "hello", execOptions{jsonOutput: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"model":"asked-model"`, `"served_model":"served-model"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("JSONL %q missing %q", out.String(), want)
		}
	}
}

// TestRunExecPromptReportsCachedPromptTokens covers issue #359: the --json
// thread.completed envelope carries the usage block, with the cache-served
// prompt tokens separable from the uncached ones.
func TestRunExecPromptReportsCachedPromptTokens(t *testing.T) {
	dir := t.TempDir()
	session := &execFakeSession{id: "s1", message: "done", usage: turn.TokenUsage{
		Available: true, SessionPromptTokens: 1000, SessionCachedPromptTokens: 800,
		SessionCompletionTokens: 100, SessionCost: 0.25,
	}}
	services := runtime.Services{App: config.AppConfig{Memory: config.MemoryConfig{Dir: dir}}.WithDefaults(), Events: events.NewOrderedDispatcher(nil), Approvals: approval.NewBroker(), Session: session}
	var out bytes.Buffer
	if err := runExecPrompt(context.Background(), services, &out, &bytes.Buffer{}, "hello", execOptions{jsonOutput: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"session_prompt_tokens":1000`, `"session_cached_prompt_tokens":800`, `"session_cost":0.25`, `"model":`, `"ts":"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("JSONL %q missing %q", out.String(), want)
		}
	}
}

func TestRunExecPromptRendersFailuresAndCanFailExit(t *testing.T) {
	dir := t.TempDir()
	session := &execFakeSession{id: "s1", message: "model said success", failures: []turn.ToolOutcome{{Tool: "demo", Kind: "error", Detail: "boom"}}, callCount: 1}
	services := runtime.Services{App: config.AppConfig{Memory: config.MemoryConfig{Dir: dir}}.WithDefaults(), Events: events.NewOrderedDispatcher(nil), Approvals: approval.NewBroker(), Session: session}
	var out bytes.Buffer
	err := runExecPrompt(context.Background(), services, &out, &bytes.Buffer{}, "hello", execOptions{failOnToolError: true})
	if err == nil || !strings.Contains(err.Error(), "1 failed or flagged tool result") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(out.String(), "[Tool attempt failures — history, not final task status]") || !strings.Contains(out.String(), "demo error: boom") {
		t.Fatalf("guard missing: %q", out.String())
	}
}

func TestRunExecPromptOutputSchemaSuppressesGuard(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	session := &execFakeSession{id: "s1", message: `{"ok":true}`, failures: []turn.ToolOutcome{{Tool: "demo", Kind: "error", Detail: "boom"}}, callCount: 1}
	services := runtime.Services{App: config.AppConfig{Memory: config.MemoryConfig{Dir: dir}}.WithDefaults(), Events: events.NewOrderedDispatcher(nil), Approvals: approval.NewBroker(), Session: session}
	var out bytes.Buffer
	if err := runExecPrompt(context.Background(), services, &out, &bytes.Buffer{}, "hello", execOptions{schemaPath: schemaPath, jsonOutput: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Tool attempt failures") || !strings.Contains(out.String(), `"tool_call_count":1`) || !strings.Contains(out.String(), `"tool_failures"`) {
		t.Fatalf("unexpected JSONL: %q", out.String())
	}
}

func TestRunExecResumeLastResolvesAndLoadsTranscript(t *testing.T) {
	dir := t.TempDir()
	// --last is scoped to the workspace root, so the seeded transcript has to be
	// stamped with the same root the services below run under.
	root := filepath.Join(dir, "workspace")
	store := sessionstore.NewForWorkspace(dir, root)
	ctx := context.Background()
	if _, err := store.Append(ctx, "sess-A", "user", "remember resume-canary-99"); err != nil {
		t.Fatal(err)
	}
	app := config.AppConfig{Memory: config.MemoryConfig{Dir: dir}, Workspace: config.WorkspaceConfig{Root: root}}.WithDefaults()
	newServices := func(session *execFakeSession) runtime.Services { return execResumeServices(app, session) }
	// --last flag: no positional session_id, all args are the prompt.
	flagSession := &execFakeSession{id: "sess-A", message: "ok"}
	if err := runExecResume(ctx, newServices(flagSession), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, []string{"what canary?"}, execOptions{lastSession: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flagSession.resumedText(), "resume-canary-99") {
		t.Fatalf("--last did not replay the transcript: %q", flagSession.resumedText())
	}
	// --resume names the session directly; the args stay prompt.
	idSession := &execFakeSession{id: "sess-A", message: "ok"}
	if err := runExecResume(ctx, newServices(idSession), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, []string{"what canary?"}, execOptions{resumeID: "sess-A"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(idSession.resumedText(), "resume-canary-99") {
		t.Fatalf("--resume did not replay the transcript: %q", idSession.resumedText())
	}
	// Neither flag is a usage error, not a panic — and a session is never named
	// positionally, so a bare prompt must not be mistaken for an ID.
	if err := runExecResume(ctx, newServices(&execFakeSession{id: "x"}), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, []string{"sess-A"}, execOptions{}); err == nil {
		t.Fatal("expected error when neither --last nor --resume is given")
	}
}

func TestRegisterExecEventsWritesToolFailureToStderr(t *testing.T) {
	bus := events.NewOrderedDispatcher(nil)
	var stderr bytes.Buffer
	stop := registerExecEvents(bus, &bytes.Buffer{}, &stderr, "s1", false)
	defer stop()
	bus.Dispatch(context.Background(), events.Event{Type: events.EventToolFailed, SessionID: "s1", Tool: &events.ToolPayload{Name: "demo", Error: "denied"}})
	if got := stderr.String(); got != "tool failed: demo: denied\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRegisterExecEventsFiltersChildSessions(t *testing.T) {
	bus := events.NewOrderedDispatcher(nil)
	var out bytes.Buffer
	stop := registerExecEvents(bus, &out, &bytes.Buffer{}, "parent", true)
	defer stop()
	bus.Dispatch(context.Background(), events.Event{Type: events.EventAfterResponse, SessionID: "child", ParentSessionID: "parent"})
	bus.Dispatch(context.Background(), events.Event{Type: events.EventAfterResponse, SessionID: "parent"})
	if got := strings.Count(out.String(), "\n"); got != 1 {
		t.Fatalf("JSONL lines = %d, want only parent event: %q", got, out.String())
	}
}

// Every --json ledger line, item events included, carries a "ts" so bench
// scoring can measure wall time between events.
func TestWriteExecEventStampsTimestamp(t *testing.T) {
	bus := events.NewOrderedDispatcher(nil)
	var out bytes.Buffer
	stop := registerExecEvents(bus, &out, &bytes.Buffer{}, "s1", true)
	defer stop()
	bus.Dispatch(context.Background(), events.Event{Type: events.EventToolStarted, SessionID: "s1", Tool: &events.ToolPayload{Name: "demo"}})
	if !strings.Contains(out.String(), `"ts":"`) {
		t.Fatalf("JSONL %q missing ts", out.String())
	}
}

// The system prompt the run was actually given lands in the --json ledger as
// its own line, so a recorded bench segment can answer which tools and skills
// were advertised (test-bench #20).
func TestWriteExecEventRecordsSystemPrompt(t *testing.T) {
	bus := events.NewOrderedDispatcher(nil)
	var out bytes.Buffer
	stop := registerExecEvents(bus, &out, &bytes.Buffer{}, "s1", true)
	defer stop()
	bus.Dispatch(context.Background(), events.Event{Type: events.EventSystemPrompt, SessionID: "s1",
		System: &events.SystemPromptPayload{Text: "You are an agent.", Chars: 17,
			Sections: []events.PromptSectionSize{{Name: "base prompt", Chars: 17}},
			Tools:    []string{"shell"}, Skills: []string{"tdd"}, Source: "default"}})

	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("decode ledger line %q: %v", out.String(), err)
	}
	if line[execwire.KeyType] != execwire.TypeSystemPrompt {
		t.Fatalf("type = %v, want %q", line[execwire.KeyType], execwire.TypeSystemPrompt)
	}
	system, ok := line[execwire.KeySystem].(map[string]any)
	if !ok {
		t.Fatalf("system payload missing from %q", out.String())
	}
	if system["text"] != "You are an agent." || system["source"] != "default" {
		t.Fatalf("system payload = %v", system)
	}
	if tools, _ := system["tools"].([]any); len(tools) != 1 || tools[0] != "shell" {
		t.Fatalf("tools = %v, want [shell]", system["tools"])
	}
	// Analysis scripts select tool events by the presence of an "item" key;
	// this line must not look like one.
	if strings.Contains(out.String(), `"item"`) {
		t.Fatalf("system-prompt line looks like a tool item: %q", out.String())
	}
}

func (s *execFakeSession) SessionID() string {
	return s.id
}

// Transcript persistence moved to the shared memory event handler
// (TestEventHandlerWritesSessionTranscript in internal/memory); exec itself only
// prints the final message now.
func TestRunExecPromptWritesFinalMessage(t *testing.T) {
	dir := t.TempDir()
	session := &execFakeSession{id: "s1", message: `{"ok":true}`}
	var out bytes.Buffer
	services := runtime.Services{
		App:       config.AppConfig{Memory: config.MemoryConfig{Dir: dir}}.WithDefaults(),
		Events:    events.NewOrderedDispatcher(nil),
		Approvals: approval.NewBroker(),
		Session:   session,
	}
	if err := runExecPrompt(context.Background(), services, &out, &bytes.Buffer{}, "hello", execOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"ok":true}` {
		t.Fatalf("unexpected stdout: %q", out.String())
	}
}

func TestExecApprovalPolicyRequiresEveryRequestedTier(t *testing.T) {
	policy := approval.NewTierPolicy(false, []string{"read-only"})
	if !policy.Approves(approval.Request{Tier: "read_workspace"}) {
		t.Fatal("read-only alias did not approve read_workspace")
	}
	if policy.Approves(approval.Request{Tier: "code_execution, read_workspace"}) {
		t.Fatal("read-only alias approved a mixed code-execution request")
	}
	if policy.Approves(approval.Request{Tier: "write_workspace"}) {
		t.Fatal("read-only alias approved a write request")
	}
}

func TestRunExecPromptWarnsWhenAllowAllEnabled(t *testing.T) {
	session := &execFakeSession{id: "s1", message: "ok"}
	services := runtime.Services{
		App:       config.AppConfig{Memory: config.MemoryConfig{Dir: t.TempDir()}}.WithDefaults(),
		Events:    events.NewOrderedDispatcher(nil),
		Approvals: approval.NewBroker(),
		Session:   session,
	}
	var errOut bytes.Buffer
	if err := runExecPrompt(context.Background(), services, &bytes.Buffer{}, &errOut, "hello", execOptions{allowAll: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "WARNING: --allow-all enables unattended arbitrary tool execution") {
		t.Fatalf("stderr missing allow-all warning: %q", errOut.String())
	}
}

func TestRunExecPromptValidatesJSONAndWritesOutputFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "answer.json")
	schemaPath := filepath.Join(dir, "schema.json")
	t.Setenv("INVOKR_HOME", dir)
	if err := os.WriteFile(schemaPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	session := &execFakeSession{id: "s1", message: `{"ok":true}`}
	services := runtime.Services{
		App:       config.AppConfig{Memory: config.MemoryConfig{Dir: dir}}.WithDefaults(),
		Events:    events.NewOrderedDispatcher(nil),
		Approvals: approval.NewBroker(),
		Session:   session,
	}
	err := runExecPrompt(context.Background(), services, &bytes.Buffer{}, &bytes.Buffer{}, "hello", execOptions{schemaPath: schemaPath, outputPath: outPath})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.seen, "Final response must be valid JSON") {
		t.Fatalf("schema instruction missing from prompt: %q", session.seen)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("unexpected output file: %q", string(data))
	}
}

// A run killed mid-turn (SIGTERM cancels the root context) must still leave a
// resumable session behind: EventAfterResponse never fires, so nothing else
// writes the transcript.
func TestRunExecPromptPersistsInterruptedSession(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "workspace")
	bus := events.NewOrderedDispatcher(nil)
	ctx, cancel := context.WithCancel(context.Background())
	session := &execCancelingSession{id: "sess-int", bus: bus, cancel: cancel}
	app := config.AppConfig{Memory: config.MemoryConfig{Dir: dir}, Workspace: config.WorkspaceConfig{Root: root}}.WithDefaults()
	services := runtime.Services{App: app, Events: bus, Approvals: approval.NewBroker(), Session: session}
	if err := runExecPrompt(ctx, services, &bytes.Buffer{}, &bytes.Buffer{}, "do the thing", execOptions{}); err == nil {
		t.Fatal("expected the cancelled run to fail")
	}
	store := sessionstore.NewForWorkspace(dir, root)
	messages, err := store.Load(context.Background(), "sess-int")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Text != "do the thing" {
		t.Fatalf("partial transcript = %+v", messages)
	}
	if !messages[1].Interrupted || !strings.Contains(messages[1].Text, "write_file") {
		t.Fatalf("interrupt marker = %+v", messages[1])
	}
	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || !sessions[0].Interrupted || !sessions[0].InCwd(root) {
		t.Fatalf("listing = %+v", sessions)
	}
}

// execCancelingSession completes one tool call, then acts like a process that
// took a SIGTERM: the context is cancelled and Send returns its error.
type execCancelingSession struct {
	id     string
	bus    events.Bus
	cancel context.CancelFunc
}

func (s *execCancelingSession) SessionID() string { return s.id }

func (s *execCancelingSession) Send(ctx context.Context, _ string) (turn.AgentResponse, error) {
	s.bus.Dispatch(ctx, events.Event{Type: events.EventToolCompleted, SessionID: s.id, Tool: &events.ToolPayload{Name: "write_file"}})
	s.cancel()
	return turn.AgentResponse{}, ctx.Err()
}

// --last picks the session by itself, so it has to say which one it picked —
// and it must pick the killed run over a newer finished one in the same
// directory.
func TestRunExecResumeLastPrefersInterruptedAndReportsTarget(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "workspace")
	store := sessionstore.NewForWorkspace(dir, root)
	ctx := context.Background()
	if _, err := store.Append(ctx, "killed", "user", "resume-canary-99"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendInterrupted(ctx, "killed", "assistant", "[interrupted]", sessionstore.ToolRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, "finished", "user", "an unrelated finished task"); err != nil {
		t.Fatal(err)
	}
	app := config.AppConfig{Memory: config.MemoryConfig{Dir: dir}, Workspace: config.WorkspaceConfig{Root: root}}.WithDefaults()
	newServices := func(session *execFakeSession) runtime.Services { return execResumeServices(app, session) }
	var errOut bytes.Buffer
	session := &execFakeSession{id: "killed", message: "ok"}
	if err := runExecResume(ctx, newServices(session), strings.NewReader(""), &bytes.Buffer{}, &errOut, []string{"continue"}, execOptions{lastSession: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.resumedText(), "resume-canary-99") {
		t.Fatalf("--last attached to the wrong session: %q", session.resumedText())
	}
	if !strings.Contains(errOut.String(), "resuming session killed (interrupted)") {
		t.Fatalf("stderr did not name the resumed session: %q", errOut.String())
	}
	var out bytes.Buffer
	if err := runExecResume(ctx, newServices(&execFakeSession{id: "killed", message: "ok"}), strings.NewReader(""), &out, &bytes.Buffer{}, []string{"continue"}, execOptions{lastSession: true, jsonOutput: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"type":"thread.started"`) || !strings.Contains(out.String(), `"status":"interrupted"`) || !strings.Contains(out.String(), `"thread_id":"killed"`) {
		t.Fatalf("ledger missing the resume target: %q", out.String())
	}
}

// Resuming twice must keep continuing one session, and must never paste a
// "Previous session transcript" preamble into the prompt — the preamble used to
// be stored verbatim and then wrapped again on the next resume, nesting one
// layer deeper each time.
func TestRunExecResumeContinuesSameSessionWithoutPreamble(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "workspace")
	store := sessionstore.NewForWorkspace(dir, root)
	ctx := context.Background()
	if _, err := store.Append(ctx, "sess-A", "user", "build the thing"); err != nil {
		t.Fatal(err)
	}
	app := config.AppConfig{Memory: config.MemoryConfig{Dir: dir}, Workspace: config.WorkspaceConfig{Root: root}}.WithDefaults()
	for _, prompt := range []string{"continue where you left off", "keep going"} {
		session := &execFakeSession{id: "sess-A", message: "ok"}
		var opened []string
		services := execResumeServices(app, session)
		inner := services.NewChatSession
		services.NewChatSession = func(id string) (turn.IdentifiedSession, *approval.Broker, error) {
			opened = append(opened, id)
			return inner(id)
		}
		if err := runExecResume(ctx, services, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, []string{prompt}, execOptions{resumeID: "sess-A"}); err != nil {
			t.Fatal(err)
		}
		if len(opened) != 1 || opened[0] != "sess-A" {
			t.Fatalf("resume opened %v, want the stored session itself", opened)
		}
		if session.seen != prompt {
			t.Fatalf("prompt = %q, want the bare request with no preamble", session.seen)
		}
		// Stand in for the curator: the answered turn lands on the same
		// transcript, which is what makes the next resume a further segment.
		if _, err := store.Append(ctx, "sess-A", "assistant", "ok"); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "sess-A" {
		t.Fatalf("resuming forked the session: %+v", sessions)
	}
	messages, err := store.Load(ctx, "sess-A")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range messages {
		if strings.Contains(msg.Text, "Previous session transcript") {
			t.Fatalf("transcript stored a resume preamble: %q", msg.Text)
		}
	}
}

func TestExecPromptRejectsEmpty(t *testing.T) {
	for _, args := range [][]string{nil, {"-"}, {"  "}} {
		if _, err := execPrompt(strings.NewReader(" \n"), args); err == nil {
			t.Fatalf("execPrompt(%q) = nil error, want empty-prompt error", args)
		}
	}
}
