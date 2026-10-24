package agentui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/surface/ui"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type fakeSession struct {
	calls     []string
	responses []turn.AgentResponse
	err       error
	compacts  int
	model     string
	models    []string
	modelsErr error
}

func (s *fakeSession) Send(_ context.Context, text string) (turn.AgentResponse, error) {
	s.calls = append(s.calls, text)
	if s.err != nil {
		return turn.AgentResponse{}, s.err
	}
	if len(s.responses) == 0 {
		return turn.AgentResponse{Message: "ok"}, nil
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

func (s *fakeSession) RequestCompaction()   { s.compacts++ }
func (s *fakeSession) SetModel(name string) { s.model = name }
func (s *fakeSession) Model() string        { return s.model }
func (s *fakeSession) ListModels(_ context.Context) ([]string, error) {
	return s.models, s.modelsErr
}

// dispatchingSession emits a tool event on the bus during Send so plain-mode
// stream routing can be observed.
type dispatchingSession struct{ bus events.Bus }

func (s *dispatchingSession) Send(ctx context.Context, _ string) (turn.AgentResponse, error) {
	s.bus.Dispatch(ctx, events.Event{Type: events.EventToolCompleted, Tool: &events.ToolPayload{Name: "read_file"}})
	return turn.AgentResponse{Message: "done"}, nil
}

type streamingSession struct {
	bus events.Bus
	out *bytes.Buffer
	t   *testing.T
}

func (s *streamingSession) Send(ctx context.Context, _ string) (turn.AgentResponse, error) {
	s.bus.Dispatch(ctx, events.Event{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: "hello "}})
	if !strings.Contains(s.out.String(), "hello ") {
		s.t.Fatal("first delta was not rendered before Send returned")
	}
	s.bus.Dispatch(ctx, events.Event{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: "world"}})
	return turn.AgentResponse{Message: "hello world", ToolFailures: []turn.ToolOutcome{{Tool: "demo", Kind: turn.OutcomeKindError, Detail: "failed"}}}, nil
}

func TestRunPlainRoutesToolEventsToStderr(t *testing.T) {
	bus := events.NewOrderedDispatcher(nil)
	var out, errOut bytes.Buffer
	if err := runPlain(context.Background(), &dispatchingSession{bus: bus}, bus, strings.NewReader("hi\nexit\n"), &out, &errOut); err != nil {
		t.Fatalf("runPlain error = %v", err)
	}
	if !strings.Contains(errOut.String(), "read_file") {
		t.Fatalf("tool event not on stderr: %q", errOut.String())
	}
	if strings.Contains(out.String(), "read_file") {
		t.Fatalf("tool event leaked to stdout: %q", out.String())
	}
}

func TestRunPlainStreamsAssistantDeltasOnce(t *testing.T) {
	bus := events.NewOrderedDispatcher(nil)
	var out bytes.Buffer
	session := &streamingSession{bus: bus, out: &out, t: t}
	if err := runPlain(context.Background(), session, bus, strings.NewReader("hi\nexit\n"), &out, io.Discard); err != nil {
		t.Fatalf("runPlain error = %v", err)
	}
	if got := strings.Count(out.String(), "hello world"); got != 1 {
		t.Fatalf("streamed response count = %d, output = %q", got, out.String())
	}
	if got := strings.Count(out.String(), "[Tool attempt failures — history, not final task status]"); got != 1 {
		t.Fatalf("tool outcome count = %d, output = %q", got, out.String())
	}
}

type fakeLogSource struct {
	lines []string
}

func (s fakeLogSource) Lines() []string {
	return s.lines
}

type fakePromptHistory struct {
	recent    []string
	appends   []string
	recentErr error
	appendErr error
}

func (h *fakePromptHistory) Recent(context.Context, int) ([]string, error) {
	return append([]string(nil), h.recent...), h.recentErr
}

func (h *fakePromptHistory) Append(_ context.Context, text string) error {
	h.appends = append(h.appends, text)
	return h.appendErr
}

func testKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

func testTextKey(text string) tea.KeyPressMsg {
	r := []rune(text)
	if len(r) == 0 {
		return tea.KeyPressMsg{}
	}
	return tea.KeyPressMsg{Code: r[0], Text: text}
}

func testModifiedKey(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func newTestModelWithLogs(lines ...string) *model {
	m := newModel(
		context.Background(),
		&fakeSession{},
		nil,
		nil,
		fakeLogSource{lines: lines},
		true,
		nil,
		nil,
	)
	m.width = 120
	m.height = 24
	return m
}

func newTestModelWithHiddenLogs(lines ...string) *model {
	m := newModel(
		context.Background(),
		&fakeSession{},
		nil,
		nil,
		fakeLogSource{lines: lines},
		false,
		nil,
		nil,
	)
	m.width = 120
	m.height = 24
	return m
}

func newTestModelWithQueue(ctx context.Context, broker *approval.Broker) *model {
	q := approval.NewApprovalQueue(ctx, broker)
	m := newModel(ctx, &fakeSession{}, nil, q, nil, false, nil, nil)
	m.width = 120
	m.height = 24
	return m
}

func TestRun_ReturnsErrorForMissingInputs(t *testing.T) {
	ctx := context.Background()
	session := &fakeSession{}
	eventBus := events.NewOrderedDispatcher(nil)
	history := &fakePromptHistory{}
	var out bytes.Buffer

	tests := []struct {
		name    string
		session turn.Session
		in      io.Reader
		out     io.Writer
		want    string
	}{
		{name: "session", session: nil, in: strings.NewReader("exit\n"), out: &out, want: "agent session is required"},
		{name: "input", session: session, in: nil, out: &out, want: "input is required"},
		{name: "output", session: session, in: strings.NewReader("exit\n"), out: nil, want: "output is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Run(ctx, Params{Session: tt.session, EventBus: eventBus, PromptHistory: history, In: tt.in, Out: tt.out})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRunScanner_ExitAndQuit(t *testing.T) {
	for _, input := range []string{"exit\n", "quit\n"} {
		session := &fakeSession{err: errors.New("should not send")}
		var out bytes.Buffer
		if err := Run(context.Background(), Params{Session: session, EventBus: events.NewOrderedDispatcher(nil), PromptHistory: &fakePromptHistory{}, In: strings.NewReader(input), Out: &out, ErrOut: io.Discard}); err != nil {
			t.Fatalf("Run(%q) error = %v", input, err)
		}
		if len(session.calls) != 0 {
			t.Fatalf("calls = %#v, want none", session.calls)
		}
		if !strings.Contains(out.String(), "agent started") {
			t.Fatalf("output missing intro: %q", out.String())
		}
	}
}

func TestRunScanner_Help(t *testing.T) {
	session := &fakeSession{err: errors.New("should not send")}
	var out bytes.Buffer
	if err := Run(context.Background(), Params{Session: session, EventBus: events.NewOrderedDispatcher(nil), PromptHistory: &fakePromptHistory{}, In: strings.NewReader("/help\nexit\n"), Out: &out, ErrOut: io.Discard}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(session.calls) != 0 {
		t.Fatalf("calls = %#v, want none", session.calls)
	}
	if !strings.Contains(out.String(), "show this help") {
		t.Fatalf("output missing help: %q", out.String())
	}
}

func TestRunScanner_SendsInput(t *testing.T) {
	session := &fakeSession{responses: []turn.AgentResponse{{Message: "hello back"}}}
	var out bytes.Buffer
	if err := Run(context.Background(), Params{Session: session, EventBus: events.NewOrderedDispatcher(nil), PromptHistory: &fakePromptHistory{}, In: strings.NewReader("hello\nexit\n"), Out: &out, ErrOut: io.Discard}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(session.calls) != 1 || session.calls[0] != "hello" {
		t.Fatalf("calls = %#v, want hello", session.calls)
	}
	if !strings.Contains(out.String(), "hello back") {
		t.Fatalf("output missing response: %q", out.String())
	}
}

func TestNarrowRenderingWrapsAndShowsToolEllipsis(t *testing.T) {
	const width = 32
	request := approval.Request{
		Tool: "shell", Tier: "code_execution", Summary: strings.Repeat("summary ", 20),
		Reason: strings.Repeat("reason ", 20), Details: strings.Repeat("detail ", 20),
	}
	renders := map[string]string{
		"intro":    renderIntro(width, 40),
		"response": renderResponseWithUsage(strings.Repeat("word ", 30), turn.TokenUsage{}, width),
		"help":     wrapText(renderCommandHelp(width), width),
		"approval": renderApprovalQueuePanel([]approval.QueueItem{{Pending: approval.Pending{Request: request}}}, 0, true, 0, defaultKeyMap(false), help.New(), width),
	}
	for name, view := range renders {
		t.Run(name, func(t *testing.T) {
			assertLinesWithinWidth(t, view, width)
		})
	}
	tool := renderToolLineWidth(glyphOK, "shell", strings.Repeat("command", 10), "1s", toolDone, width)
	if !strings.Contains(tool, "…") {
		t.Fatalf("tool line missing visible ellipsis: %q", tool)
	}
	assertLinesWithinWidth(t, tool, width)
}

func assertLinesWithinWidth(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line width = %d, want <= %d: %q", got, width, line)
		}
	}

}

func TestApprovalRenderingPreservesSummaryAndExpandedFields(t *testing.T) {
	request := approval.Request{
		Tool: "shell-tool", Tier: "write-tier", Summary: strings.Repeat("unabridgedtoken ", 15),
		Reason: "specific reason", Details: "complete command details",
	}
	item := approval.QueueItem{Pending: approval.Pending{Request: request}}

	collapsed := ansi.Strip(renderApprovalQueuePanel([]approval.QueueItem{item}, 0, false, 0, defaultKeyMap(false), help.New(), 40))
	if got := strings.Count(collapsed, "unabridgedtoken"); got != 15 {
		t.Fatalf("collapsed summary token count = %d, want 15: %q", got, collapsed)
	}

	expanded := ansi.Strip(renderApprovalQueuePanel([]approval.QueueItem{item}, 0, true, 0, defaultKeyMap(false), help.New(), 40))
	expanded += ansi.Strip(renderApprovalQueuePanel([]approval.QueueItem{item}, 0, true, 100, defaultKeyMap(false), help.New(), 40))
	for _, want := range []string{
		"shell-tool", "write-tier", "Summary: unabridgedtoken",
		"Reason: specific reason", "Details:", "complete command details",
	} {
		if !strings.Contains(expanded, want) {
			t.Errorf("expanded approval missing %q: %q", want, expanded)
		}
	}
}

func TestApprovalPanelRendersFieldGridAndDimNeighbours(t *testing.T) {
	const width = 80
	selected := approval.Request{
		Tool: "shell", Tier: "code_execution",
		Summary: "Run a shell command", Subject: "git clean -fd",
	}
	queued := approval.Request{Tool: "apply_patch", Tier: "write", Summary: "Edit main.go"}
	panel := renderApprovalQueuePanel([]approval.QueueItem{
		{Pending: approval.Pending{Request: selected}},
		{Pending: approval.Pending{Request: queued}},
	}, 0, false, 0, defaultKeyMap(false), help.New(), width)
	plain := ansi.Strip(panel)

	for _, want := range []string{"Run a shell command", "git clean -fd"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("approval panel missing field %q:\n%s", want, plain)
		}
	}
	// The tier is a pill now, not a bracketed tag.
	if strings.Contains(plain, "[code_execution]") {
		t.Fatalf("tier still rendered as a bracketed tag:\n%s", plain)
	}
	if !strings.Contains(panel, ui.Invokr.Pill(ui.Invokr.Warn, "code execution")) {
		t.Fatalf("tier is not on a pill:\n%q", panel)
	}
	// A queued neighbour is one dim row, without its own field grid.
	if !strings.Contains(plain, "apply_patch  Edit main.go") {
		t.Fatalf("queued neighbour missing its row:\n%s", plain)
	}
	if strings.Count(plain, "Run a shell command") != 1 {
		t.Fatalf("neighbour drew a field grid of its own:\n%s", plain)
	}
	if !strings.Contains(plain, "Y yes all") {
		t.Fatalf("a queue of two lacks yes-to-all:\n%s", plain)
	}
	if !strings.Contains(plain, `a always "git clean *"`) {
		t.Fatalf("panel missing the always-allow affordance:\n%s", plain)
	}
	assertLinesWithinWidth(t, panel, width)
}

func TestAlwaysAllowGrantsTheSubjectThenApproves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)
	m.grants = approval.NewStore(filepath.Join(t.TempDir(), "grants.json"))

	done := make(chan approval.Decision, 1)
	go func() {
		decision, _ := broker.RequestApproval(ctx, approval.Request{
			Tool: "shell", Tier: "code_execution",
			Summary: "Run a shell command", Subject: "git clean -fd",
		})
		done <- decision
	}()
	waitForQueueLen(t, m.approvalQueue, 1)

	m.updateKey(testTextKey("a")) //nolint:errcheck
	if decision := <-done; !decision.Approved {
		t.Fatalf("always-allow did not approve the pending request: %+v", decision)
	}
	grants := m.grants.List()
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}
	if grants[0].Tool != "shell" || grants[0].ArgPattern != "git clean *" {
		t.Fatalf("grant = %+v, want it bound to the pattern derived from the subject", grants[0])
	}
}

func TestAlwaysAllowRefusesARequestWithNoSubject(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)
	m.grants = approval.NewStore(filepath.Join(t.TempDir(), "grants.json"))

	go broker.RequestApproval(ctx, approval.Request{Tool: "shell", Summary: "no subject"}) //nolint:errcheck
	waitForQueueLen(t, m.approvalQueue, 1)

	m.updateKey(testTextKey("a")) //nolint:errcheck
	// Nothing standing grants could match, so the request must stay pending
	// rather than be approved under a grant that would never fire again.
	if got := m.approvalQueue.Len(); got != 1 {
		t.Fatalf("queue length = %d, want the request left pending", got)
	}
	if got := len(m.grants.List()); got != 0 {
		t.Fatalf("grant count = %d, want none", got)
	}
	if !strings.Contains(ansi.Strip(m.chatContent()), "no safe pattern") {
		t.Fatalf("no explanation in the transcript: %q", ansi.Strip(m.chatContent()))
	}
}

func TestToggleHintsCollapsesTheFooterToOneKey(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.hasUsage = true
	m.usage = turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 200}
	if !strings.Contains(ansi.Strip(m.footer()), "alt+x browse details") {
		t.Fatalf("missing details navigation tip: %s", m.footer())
	}

	if !strings.Contains(ansi.Strip(m.footer()), "enter send") {
		t.Fatalf("hints are not showing to begin with: %q", ansi.Strip(m.footer()))
	}

	m.updateKey(testModifiedKey('g', tea.ModCtrl)) //nolint:errcheck
	hidden := ansi.Strip(m.footer())
	if strings.Contains(hidden, "enter send") || strings.Contains(hidden, "ctrl+h help") {
		t.Fatalf("footer still carries the key hints: %q", hidden)
	}
	// The toggle itself must survive, or there is no way back.
	if !strings.Contains(hidden, "ctrl+g show hints") {
		t.Fatalf("footer lost the affordance that restores the hints: %q", hidden)
	}
	// Hiding the hints must not take the context readout with it — it lives on
	// the statusline under the input, not in the footer.
	if !strings.Contains(ansi.Strip(m.statusRow()), "ctx ") {
		t.Fatalf("context readout disappeared with the hints: %q", ansi.Strip(m.statusRow()))
	}

	m.updateKey(testModifiedKey('g', tea.ModCtrl)) //nolint:errcheck
	if !strings.Contains(ansi.Strip(m.footer()), "enter send") {
		t.Fatalf("ctrl+g did not restore the hints: %q", ansi.Strip(m.footer()))
	}
}

func TestHintsHiddenBeforeAnyUsageLeavesOnlyTheToggle(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

	m.updateKey(testModifiedKey('g', tea.ModCtrl)) //nolint:errcheck
	// No turn has reported usage yet, so the context cell is empty too — the
	// footer is down to the one key that brings the hints back, on the right.
	got := ansi.Strip(m.footer())
	if strings.TrimSpace(got) != "ctrl+g show hints" {
		t.Fatalf("footer = %q, want just the restore key", got)
	}
	if !strings.HasPrefix(got, " ") {
		t.Fatalf("footer = %q, want the restore key right-aligned", got)
	}
}

func TestContextReadoutHasNoHideToggle(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.hasUsage = true
	m.usage = turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 200}

	// ctrl+b was the old "hide the context bar" key, back when the readout took
	// two full rows above the input. It is now one cell on a row that already
	// exists, so the key is gone and ctrl+b belongs to the prompt again.
	m.updateKey(testModifiedKey('b', tea.ModCtrl)) //nolint:errcheck
	if !strings.Contains(ansi.Strip(m.statusRow()), "ctx ") {
		t.Fatalf("ctrl+b still hides the context readout: %q", ansi.Strip(m.statusRow()))
	}
	for _, row := range defaultKeyMap(false).FullHelp() {
		for _, binding := range row {
			if binding.Help().Key == "ctrl+b" {
				t.Fatalf("keymap still advertises ctrl+b: %q", binding.Help().Desc)
			}
		}
	}
}

func TestHelpUnhidesTheKeyHints(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

	m.updateKey(testModifiedKey('g', tea.ModCtrl)) //nolint:errcheck
	m.updateKey(testModifiedKey('h', tea.ModCtrl)) //nolint:errcheck
	// ctrl+h expands the help; doing that into a hidden footer would read as a
	// dead key, so asking for help draws the hints for as long as it is open.
	if !strings.Contains(ansi.Strip(m.footer()), "scroll up") {
		t.Fatalf("expanded help is not visible: %q", ansi.Strip(m.footer()))
	}
	// The ctrl+g preference is untouched, so dismissing the help hides them again.
	if !m.hideHints {
		t.Fatal("ctrl+h cleared the stored ctrl+g preference")
	}
	m.updateKey(testKey(tea.KeyEscape)) //nolint:errcheck
	if got := strings.TrimSpace(ansi.Strip(m.footer())); got != "ctrl+g show hints" {
		t.Fatalf("footer after dismissing help = %q, want the hints hidden again", got)
	}
}

func TestNarrowStatusKeepsHelpHint(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	footer := ansi.Strip(m.footer())
	if !strings.Contains(footer, "ctrl+h help") {
		t.Fatalf("narrow footer missing help hint: %q", footer)
	}
	if strings.Contains(footer, "enter send") {
		t.Fatalf("narrow footer retained optional hint: %q", footer)
	}
	// The hints live in the footer now, not inside the scrolling transcript.
	if strings.Contains(ansi.Strip(m.chatContent()), "ctrl+h help") {
		t.Fatalf("help hint leaked into transcript: %q", m.chatContent())
	}
}

func TestUsageRendersOnceInBottomStatusLine(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.updateResponse(responseMsg{text: "first", usage: turn.TokenUsage{Available: true, LatestPromptTokens: 100, SessionTotalTokens: 100}})
	m.updateResponse(responseMsg{text: "second", usage: turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 200, SessionTotalTokens: 500}})

	if content := ansi.Strip(m.chatContent()); strings.Contains(content, "ctx ") {
		t.Fatalf("usage leaked into transcript: %q", content)
	}
	view := ansi.Strip(m.mainView())
	wantMeter := strings.Repeat("█", 3) + strings.Repeat("░", 13) + " 200 / 1k"
	if strings.Count(view, "ctx ") != 1 || !strings.Contains(view, "ctx "+wantMeter) || strings.Contains(view, "session 500 tokens") {
		t.Fatalf("bottom status = %q", view)
	}
	// The readout is the bottom-right corner: last row, hard against the edge.
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "ctx ") {
		t.Fatalf("context readout is not on the bottom row: %q", last)
	}
	if !strings.HasSuffix(last, "200 / 1k") {
		t.Fatalf("context readout is not against the right edge: %q", last)
	}
}

func TestModel_UpArrowReturnsPreviousInput(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.history = []string{"first"}

	updated, _ := m.Update(testKey(tea.KeyUp))
	got := updated.(*model).input.Value()
	if got != "first" {
		t.Fatalf("input after up = %q, want %q", got, "first")
	}
}

func TestModel_UpArrowReturnsPreloadedInput(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, []string{"older", "newer"})

	updated, _ := m.Update(testKey(tea.KeyUp))
	got := updated.(*model).input.Value()
	if got != "newer" {
		t.Fatalf("input after up = %q, want %q", got, "newer")
	}
}

func TestModel_DownArrowWithoutHistoryBrowseKeepsCurrentInput(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.input.SetValue("abc")

	updated, _ := m.Update(testKey(tea.KeyDown))
	got := updated.(*model).input.Value()
	if got != "abc" {
		t.Fatalf("input after down = %q, want %q", got, "abc")
	}
}

func TestModel_AltEnterAddsNewlineWithoutSubmitting(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, &fakePromptHistory{}, nil)
	m.input.SetValue("first")
	m.input.CursorEnd()

	updated, _ := m.Update(testModifiedKey(tea.KeyEnter, tea.ModAlt))
	got := updated.(*model)
	if got.input.Value() != "first\n" {
		t.Fatalf("input = %q, want multiline draft", got.input.Value())
	}
	if got.sending || len(session.calls) != 0 {
		t.Fatalf("alt+enter submitted draft: sending=%t calls=%#v", got.sending, session.calls)
	}
}

func TestModel_EnterSubmitsMultilineInput(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, &fakePromptHistory{}, nil)
	m.input.SetValue("first\nsecond")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	got := updated.(*model)
	if !got.sending || cmd == nil {
		t.Fatalf("enter did not submit: sending=%t cmd=%v", got.sending, cmd)
	}
	drainSubmit(t, got, cmd)
	if len(session.calls) != 1 || session.calls[0] != "first\nsecond" {
		t.Fatalf("calls = %#v, want multiline prompt", session.calls)
	}
}

func TestModel_SubmitPersistsPromptHistoryAndDedupesModelHistory(t *testing.T) {
	history := &fakePromptHistory{}
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, history, []string{"hello", "older"})
	m.input.SetValue("hello")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	got := updated.(*model)
	if len(got.history) != 2 || got.history[0] != "older" || got.history[1] != "hello" {
		t.Fatalf("history = %#v, want deduped newest hello", got.history)
	}
	if cmd == nil {
		t.Fatal("submit returned nil command, want send/persist command")
	}
	msg := got.persistHistoryCmd("hello")()
	if saved, ok := msg.(historySavedMsg); !ok || saved.err != nil {
		t.Fatalf("persistHistoryCmd() = %#v, want successful historySavedMsg", msg)
	}
	if len(history.appends) != 1 || history.appends[0] != "hello" {
		t.Fatalf("history appends = %#v, want hello", history.appends)
	}
}

func TestModel_SubmitHelpDoesNotCallSession(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)
	m.input.SetValue("/help")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	got := updated.(*model)
	if cmd != nil {
		t.Fatalf("help returned command, want nil")
	}
	if !got.overlay.IsOpen() || got.overlay.Title() != "help" {
		t.Fatalf("overlay = open:%v title:%q, want the help popup", got.overlay.IsOpen(), got.overlay.Title())
	}
	if len(got.chatLog.messages) != 0 {
		t.Fatalf("help must not touch the transcript: %#v", got.chatLog.messages)
	}
	if len(session.calls) != 0 {
		t.Fatalf("calls = %#v, want none", session.calls)
	}
}

// TestModel_SubmitBareHelpAliasShowsHelp covers the bare "help" alias
// (no slash), which runCommand never sees since it only handles
// slash-prefixed input; submit() must still show help for it.
func TestModel_SubmitBareHelpAliasShowsHelp(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)
	m.input.SetValue("help")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	got := updated.(*model)
	if cmd != nil {
		t.Fatalf("help returned command, want nil")
	}
	if !got.overlay.IsOpen() || got.overlay.Title() != "help" {
		t.Fatalf("overlay = open:%v title:%q, want the help popup", got.overlay.IsOpen(), got.overlay.Title())
	}
	if len(got.chatLog.messages) != 0 {
		t.Fatalf("help must not touch the transcript: %#v", got.chatLog.messages)
	}
	if len(session.calls) != 0 {
		t.Fatalf("calls = %#v, want none", session.calls)
	}
}

// The popup is modal: while it is up esc dismisses it rather than interrupting
// the turn underneath, and the transcript keeps rendering behind it.
func TestModel_OverlayIsModalAndFloatsOverChat(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 52})
	m = updated.(*model)
	m.chatLog.Append(message{role: "user", text: "unmistakable-user-line"})
	m.sending = true
	m.input.SetValue("/help")
	updated, _ = m.Update(testKey(tea.KeyEnter))
	got := updated.(*model)

	view := ansi.Strip(got.View().Content)
	// The frame around the popup is still the chat's — the popup replaced
	// nothing, it is drawn on top.
	if !strings.Contains(view, "unmistakable-user-line") || !strings.Contains(view, "ask anything") {
		t.Fatalf("chat should still be visible behind the popup:\n%s", view)
	}
	if !strings.Contains(view, "esc close") {
		t.Fatalf("popup missing its dismiss hint:\n%s", view)
	}

	updated, _ = got.Update(testKey(tea.KeyEscape))
	got = updated.(*model)
	if got.overlay.IsOpen() {
		t.Fatalf("esc should close the popup")
	}
	if !got.sending {
		t.Fatalf("esc closing the popup must not interrupt the turn")
	}
}

func TestModel_SubmitCompactWhileSendingRequestsCompaction(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)
	m.sending = true
	m.input.SetValue("/compact")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	got := updated.(*model)
	if cmd != nil || session.compacts != 1 {
		t.Fatalf("cmd = %v, compactions = %d", cmd, session.compacts)
	}
	if len(got.chatLog.messages) != 1 || !strings.Contains(got.chatLog.messages[0].text, "nothing to compact yet") {
		t.Fatalf("messages = %#v", got.chatLog.messages)
	}
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestModel_CtrlCStopsRunningTaskThenQuitsWhenIdle(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)

	// Idle: the first ctrl+c only arms the confirmation, the second quits.
	if _, cmd := m.updateKey(testModifiedKey('c', tea.ModCtrl)); isQuit(cmd) {
		t.Fatalf("first idle ctrl+c should not quit")
	}
	if hint := ansi.Strip(m.helpHint()); !strings.Contains(hint, "ctrl+c again") {
		t.Fatalf("armed footer should hint at the second press, got %q", hint)
	}
	if _, cmd := m.updateKey(testModifiedKey('c', tea.ModCtrl)); !isQuit(cmd) {
		t.Fatalf("second idle ctrl+c should quit")
	}
	m.quitArmed = time.Time{}

	// Running: ctrl+c cancels the task and stays open.
	cancelled := false
	m.sending = true
	m.cancelSend = func() { cancelled = true }
	_, cmd := m.updateKey(testModifiedKey('c', tea.ModCtrl))
	if isQuit(cmd) {
		t.Fatalf("ctrl+c while running should not quit")
	}
	if !cancelled || !m.stopping {
		t.Fatalf("ctrl+c while running should cancel (cancelled=%v stopping=%v)", cancelled, m.stopping)
	}

	// The cancelled Send returns; the response renders a stop notice, not an error.
	m.updateResponse(responseMsg{err: context.Canceled})
	if m.sending {
		t.Fatalf("sending should clear after the stopped response")
	}
	last := m.chatLog.messages[len(m.chatLog.messages)-1]
	if last.role != "tool" || !strings.Contains(last.text, "stopped") {
		t.Fatalf("expected stop notice, got %+v", last)
	}
}

func TestModel_CtrlC_ClearsQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
			errs <- err
		}()
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if m.approvalQueue.Len() == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if m.approvalQueue.Len() != 2 {
		t.Fatalf("expected 2 items in queue, got %d", m.approvalQueue.Len())
	}

	// ctrl+c while queue non-empty: denies all, stays open
	_, cmd := m.updateKey(testModifiedKey('c', tea.ModCtrl))
	if isQuit(cmd) {
		t.Fatalf("ctrl+c while queue non-empty should not quit")
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			var denied *approval.DeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected DeniedError, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("goroutine did not unblock after interrupt")
		}
	}

	// ctrl+c again (queue empty, not sending): arms, then quits
	m.updateKey(testModifiedKey('c', tea.ModCtrl))
	if _, cmd2 := m.updateKey(testModifiedKey('c', tea.ModCtrl)); !isQuit(cmd2) {
		t.Fatalf("ctrl+c with empty queue and not sending should quit")
	}
}

func TestModel_LongResponseStaysWithinTerminalHeight(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.width = 80
	m.height = 10
	m.hasUsage = true
	m.usage = turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 500}
	m.chatLog.Append(message{role: "assistant", text: strings.Repeat("tool output line\n", 80)})

	view := m.View().Content
	if got := lipgloss.Height(view); got > m.height {
		t.Fatalf("view height = %d, want <= %d\n%s", got, m.height, view)
	}
	if !strings.Contains(view, renderPrompt()) {
		t.Fatalf("view missing input prompt after long response: %q", view)
	}
}

func TestRenderResponseWithUsageLimit(t *testing.T) {
	view := renderResponseWithUsage("ok", turn.TokenUsage{
		Available:              true,
		LatestPromptTokens:     64000,
		LatestCompletionTokens: 100,
		ContextWindowTokens:    128000,
		TurnTotalTokens:        128200,
		SessionTotalTokens:     128200,
	}, 0)

	if !strings.Contains(view, "context: 64,100 / 128,000 tokens (50%)") {
		t.Fatalf("view missing context usage: %q", view)
	}
	if strings.Contains(view, "session 128,200 tokens") {
		t.Fatalf("view exposed cumulative session usage: %q", view)
	}
}

func TestRenderContextStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		used  int64
		style lipgloss.Style
		count string
	}{
		{name: "normal", used: 690, style: ui.Invokr.Info, count: "690 / 1k"},
		{name: "warning", used: 700, style: ui.Invokr.Warn, count: "700 / 1k"},
		{name: "compact", used: 850, style: ui.Invokr.Danger, count: "850 / 1k"},
		{name: "overflow", used: 1100, style: ui.Invokr.Danger, count: "1.1k / 1k"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := renderContextStatus(turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: tc.used}, 80, 80)
			// The meter is drawn by tuikit.Meter, so assert on the visible cell
			// count and on the label's style, not on the fill's escape codes.
			if got := strings.Count(ansi.Strip(status), "█") + strings.Count(ansi.Strip(status), "░"); got != contextMeterWidth {
				t.Fatalf("meter cells = %d, want %d: %q", got, contextMeterWidth, status)
			}
			if !strings.HasPrefix(ansi.Strip(status), "ctx ") {
				t.Fatalf("status missing ctx label: %q", ansi.Strip(status))
			}
			if !strings.HasSuffix(status, tc.style.Render(tc.count)) {
				t.Fatalf("status count has wrong style or text: %q", status)
			}
		})
	}

	unknown := ansi.Strip(renderContextStatus(turn.TokenUsage{Available: true, LatestPromptTokens: 100}, 80, 80))
	if unknown != "ctx 100 / ?" {
		t.Fatalf("unknown window status = %q", unknown)
	}
	if got := renderContextStatus(turn.TokenUsage{}, 80, 80); got != "" {
		t.Fatalf("unavailable usage rendered a status: %q", got)
	}
	// A pane too narrow for the meter keeps the numbers and drops the bar.
	narrow := renderContextStatus(turn.TokenUsage{Available: true, ContextWindowTokens: 1000, LatestPromptTokens: 500}, 14, 80)
	if ansi.StringWidth(narrow) > 14 {
		t.Fatalf("narrow status width = %d, want <= 14: %q", ansi.StringWidth(narrow), narrow)
	}
	if strings.Contains(ansi.Strip(narrow), "█") {
		t.Fatalf("narrow status kept the meter: %q", ansi.Strip(narrow))
	}
}

func TestRenderToolEvent(t *testing.T) {
	started := renderToolEvent(events.Event{
		Type: events.EventToolStarted,
		Tool: &events.ToolPayload{Name: "file_search", Turn: 0, Arguments: `{"query":"main.go"}`},
	})
	if !strings.Contains(started, "search") || !strings.Contains(started, "main.go") || !strings.Contains(started, "running...") {
		t.Fatalf("started tool event = %q", started)
	}

	completed := renderToolEvent(events.Event{
		Type: events.EventToolCompleted,
		Tool: &events.ToolPayload{Name: "file_search", Duration: 12 * time.Millisecond, OutputBytes: 842},
	})
	if !strings.Contains(completed, "search") || !strings.Contains(completed, "12ms") || !strings.Contains(completed, "842 B") {
		t.Fatalf("completed tool event = %q", completed)
	}

	coded := renderToolEvent(events.Event{
		Type: events.EventToolCompleted,
		Tool: &events.ToolPayload{Name: "shell", Duration: 5 * time.Millisecond, Arguments: `{"command":"ls","args":["/nope"]}`, Code: "exit 1"},
	})
	if !strings.Contains(coded, "[exit 1]") || !strings.Contains(coded, "/nope") {
		t.Fatalf("coded tool event = %q", coded)
	}
}

func TestAssistantAnswerIsIndentedUnderItsTurn(t *testing.T) {
	log := newChatLog(progressAll)
	log.Append(message{role: "user", text: "hi"})
	log.Append(message{role: "assistant", text: "Hey! What can I help with?"})

	lines := strings.Split(strings.TrimSpace(ansi.Strip(log.Content(60, ""))), "\n")
	// rule is empty in the current style, so the turn opens straight on the caret.
	if !strings.HasPrefix(lines[0], rule+"› hi") {
		t.Fatalf("user line = %q", lines[0])
	}
	answer := lines[len(lines)-1]
	if !strings.HasPrefix(answer, answerIndent+"Hey!") {
		t.Fatalf("answer is not indented under its turn: %q", answer)
	}
}

func TestRenderThinkingShowsElapsedAndInterrupt(t *testing.T) {
	idle := renderThinking("◐", time.Time{}, time.Time{}, 60)
	if ansi.Strip(idle) != "◐ thinking" {
		t.Fatalf("thinking without a start time = %q", ansi.Strip(idle))
	}
	if !strings.HasPrefix(idle, ui.Invokr.Info.Render("◐ thinking")) {
		t.Fatalf("thinking is not in the running color: %q", idle)
	}
	started := time.Now().Add(-1800 * time.Millisecond)
	live := ansi.Strip(renderThinking("◐", started, started, 60))
	if !strings.Contains(live, "1.8s · esc to interrupt") {
		t.Fatalf("thinking = %q, want live elapsed and the interrupt hint", live)
	}
}

// TestRenderThinkingNamesASilentServer: a turn with no output for a while says
// the model server has not replied, so a dead server reads differently from a
// slow model; any output resets the clock.
func TestRenderThinkingNamesASilentServer(t *testing.T) {
	started := time.Now().Add(-45 * time.Second)
	silent := ansi.Strip(renderThinking("◐", started, started, 80))
	if !strings.Contains(silent, "waiting for the model server") || !strings.Contains(silent, "no output for 45") {
		t.Fatalf("silent turn = %q", silent)
	}
	recent := ansi.Strip(renderThinking("◐", started, time.Now().Add(-2*time.Second), 80))
	if !strings.Contains(recent, "◐ thinking") {
		t.Fatalf("turn with recent output = %q, want plain thinking", recent)
	}
}

func TestEscInterruptsAnInFlightTurnOnly(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.sending = true
	canceled := false
	m.cancelSend = func() { canceled = true }

	if _, cmd := m.updateKey(testKey(tea.KeyEscape)); isQuit(cmd) {
		t.Fatalf("esc during a turn should interrupt, not quit")
	}
	if !canceled || !m.stopping {
		t.Fatalf("esc did not cancel the in-flight send (canceled=%v stopping=%v)", canceled, m.stopping)
	}

	// With nothing in flight, esc is the input's key, not an interrupt.
	idle := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	if _, cmd := idle.updateKey(testKey(tea.KeyEscape)); isQuit(cmd) {
		t.Fatalf("esc with no turn in flight should not quit")
	}
}

func TestRenderToolBlockColumns(t *testing.T) {
	const width = 80
	done := message{
		role: "tool", toolName: "shell", toolArgs: `{"command":"ls","args":["-la"]}`,
		toolStatus: toolDone, toolDuration: 65 * time.Millisecond, toolBytes: 3100,
	}
	line := renderToolBlock(done, "", width)
	if got := ansi.StringWidth(line); got != width {
		t.Fatalf("done tool line width = %d, want %d (metadata right-aligned): %q", got, width, ansi.Strip(line))
	}
	if !strings.HasSuffix(ansi.Strip(line), "65ms · 3.1 KB") {
		t.Fatalf("done tool line = %q", ansi.Strip(line))
	}
	if !strings.HasPrefix(line, ui.Invokr.Success.Render(glyphOK)) {
		t.Fatalf("done glyph is not success-colored: %q", line)
	}

	failed := message{
		role: "tool", toolName: "web_search", toolArgs: `{"query":"trees"}`,
		toolStatus: toolFailed, toolDuration: 300 * time.Millisecond,
		toolError: "all providers failed — tavily: 401 unauthorized",
	}
	block := renderToolBlock(failed, "", width)
	head, detail, found := strings.Cut(ansi.Strip(block), "\n")
	if !found {
		t.Fatalf("failed tool block has no detail sub-block: %q", block)
	}
	if !strings.HasSuffix(head, "300ms · failed") {
		t.Fatalf("failed tool head = %q", head)
	}
	if !strings.HasPrefix(detail, "   │ ") || !strings.Contains(detail, "401 unauthorized") {
		t.Fatalf("failed tool detail = %q", detail)
	}
	if !strings.HasPrefix(block, ui.Invokr.Danger.Render(glyphFail)) {
		t.Fatalf("failed glyph is not danger-colored: %q", block)
	}
	assertLinesWithinWidth(t, block, width)

	// A running block is blue and reports live elapsed plus the stop key.
	running := message{
		role: "tool", toolName: "shell", toolArgs: `{"command":"go","args":["build","./..."]}`,
		toolStatus: toolRunning, toolStartedAt: time.Now().Add(-4 * time.Second),
	}
	live := renderToolBlock(running, "◐", width)
	if !strings.Contains(ansi.Strip(live), "running 4.0s · esc stop") {
		t.Fatalf("running tool line = %q", ansi.Strip(live))
	}
	if !strings.HasPrefix(live, ui.Invokr.Info.Render("◐")) {
		t.Fatalf("running glyph is not info-colored: %q", live)
	}
}

func TestApplyToolEventUpdatesInPlace(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &events.ToolPayload{ID: "call_1", Name: "shell", Arguments: `{"command":"ls"}`}})
	if len(m.chatLog.messages) != 1 || m.chatLog.messages[0].toolStatus != toolRunning {
		t.Fatalf("after start: %#v", m.chatLog.messages)
	}
	m.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &events.ToolPayload{ID: "call_1", Name: "shell", Duration: 7 * time.Millisecond, OutputBytes: 12}})
	if len(m.chatLog.messages) != 1 {
		t.Fatalf("completed should mutate in place, got %d messages", len(m.chatLog.messages))
	}
	if m.chatLog.messages[0].toolStatus != toolDone || m.chatLog.messages[0].toolDuration != 7*time.Millisecond {
		t.Fatalf("block not updated: %#v", m.chatLog.messages[0])
	}
}

func TestAssistantDeltaFinalizesWithoutDuplicate(t *testing.T) {
	m := newTestModelWithLogs()
	m.chatLog.ApplyEvent(events.Event{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: "hel"}})
	m.chatLog.ApplyEvent(events.Event{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: "lo"}})
	if len(m.chatLog.messages) != 1 || m.chatLog.messages[0].text != "hello" || !m.chatLog.messages[0].streaming {
		t.Fatalf("messages = %#v", m.chatLog.messages)
	}
	_, _ = m.updateResponse(responseMsg{text: "hello"})
	if len(m.chatLog.messages) != 1 || m.chatLog.messages[0].text != "hello" || m.chatLog.messages[0].streaming {
		t.Fatalf("final messages = %#v", m.chatLog.messages)
	}
}

func waitForQueueLen(t *testing.T, q interface{ Len() int }, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Len() == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for queue len=%d, got %d", n, q.Len())
}

func TestModel_QueueRendersWhenItemsPresent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)

	for i := 0; i < 2; i++ {
		go func() {
			_, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		}()
	}
	waitForQueueLen(t, m.approvalQueue, 2)

	content := m.chatContent()
	if !strings.Contains(content, "APPROVAL") || !strings.Contains(content, "1 of 2") {
		t.Fatalf("expected approval queue panel in chatContent, got: %q", content)
	}
}

func TestModel_NarrowApprovalFlowShowsDetailsBeforeDecision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 48, Height: 40})

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{
			Tool: "shell", Tier: "code_execution",
			Summary: strings.Repeat("inspectallarguments ", 6),
			Reason:  "command changes workspace state",
			Details: `git clean -fd -- path with spaces`,
		})
		done <- err
	}()
	waitForQueueLen(t, m.approvalQueue, 1)

	collapsed := m.View().Content
	assertLinesWithinWidth(t, collapsed, 48)
	if got := strings.Count(ansi.Strip(collapsed), "inspectallarguments"); got != 6 {
		t.Fatalf("visible summary token count = %d, want 6: %q", got, collapsed)
	}

	m.updateKey(testTextKey("v")) //nolint:errcheck
	expanded := ansi.Strip(m.View().Content)
	for _, want := range []string{"code execution", "Reason: command changes", "Details:", "git clean -fd -- path with spaces"} {
		if !strings.Contains(expanded, want) {
			t.Errorf("expanded approval missing %q: %q", want, expanded)
		}
	}

	m.updateKey(testTextKey("n")) //nolint:errcheck
	if err := <-done; err == nil {
		t.Fatal("approval succeeded after deny")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "approval denied: shell") {
		t.Fatalf("denial missing from transcript: %q", view)
	}
}

func TestModel_QueueApproveItem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()
	waitForQueueLen(t, m.approvalQueue, 1)

	m.updateKey(testTextKey("y")) //nolint:errcheck
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil error (approved), got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("goroutine did not unblock after approve")
	}
	if m.approvalQueue.Len() != 0 {
		t.Fatalf("expected empty queue after approve, got %d", m.approvalQueue.Len())
	}
}

func TestModel_QueueDenyItem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()
	waitForQueueLen(t, m.approvalQueue, 1)

	m.updateKey(testTextKey("n")) //nolint:errcheck
	select {
	case err := <-done:
		var denied *approval.DeniedError
		if !errors.As(err, &denied) {
			t.Fatalf("expected DeniedError, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("goroutine did not unblock after deny")
	}
}

func TestModel_QueueApproveAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)

	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
			errs <- err
		}()
	}
	waitForQueueLen(t, m.approvalQueue, 3)

	m.updateKey(testTextKey("Y")) //nolint:errcheck
	for i := 0; i < 3; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("expected nil error (approved), got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("goroutine did not unblock after approve-all")
		}
	}
}

func TestModel_QueueDenyAll_via_D(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
			errs <- err
		}()
	}
	waitForQueueLen(t, m.approvalQueue, 2)

	m.updateKey(testTextKey("N")) //nolint:errcheck
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			var denied *approval.DeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected DeniedError, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("goroutine did not unblock after deny-all")
		}
	}
}

func TestModel_QueueDenyAll_via_Esc(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
			errs <- err
		}()
	}
	waitForQueueLen(t, m.approvalQueue, 2)

	m.updateKey(tea.KeyPressMsg{Code: tea.KeyEscape}) //nolint:errcheck
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			var denied *approval.DeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected DeniedError, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("goroutine did not unblock after esc deny-all")
		}
	}
}

func TestModel_QueueCursorNav(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)

	for i := 0; i < 3; i++ {
		go func() {
			_, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		}()
	}
	waitForQueueLen(t, m.approvalQueue, 3)

	m.approvals.expanded = true
	m.updateKey(testTextKey("j")) //nolint:errcheck
	if m.approvals.expanded {
		t.Fatal("queue expansion remained open after cursor moved")
	}
	m.updateKey(testTextKey("j")) //nolint:errcheck
	if m.approvals.cursor != 2 {
		t.Fatalf("expected cursor=2 after two j presses, got %d", m.approvals.cursor)
	}
	m.updateKey(testTextKey("k")) //nolint:errcheck
	if m.approvals.cursor != 1 {
		t.Fatalf("expected cursor=1 after k press, got %d", m.approvals.cursor)
	}
	m.approvalQueue.DecideAll(approval.Decision{Approved: true})
}

func TestModel_ApprovalDetailsScrollToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	m := newTestModelWithQueue(ctx, broker)
	details := make([]string, 30)
	for i := range details {
		details[i] = fmt.Sprintf("+line %02d", i)
	}
	go func() {
		_, _ = broker.RequestApproval(ctx, approval.Request{Tool: "apply_patch", Details: strings.Join(details, "\n")})
	}()
	waitForQueueLen(t, m.approvalQueue, 1)

	m.updateKey(testTextKey("v")) //nolint:errcheck
	initial := ansi.Strip(m.chatContent())
	if !strings.Contains(initial, "+line 00") || strings.Contains(initial, "+line 29") {
		t.Fatalf("initial detail page = %q", initial)
	}
	m.updateKey(testKey(tea.KeyEnd)) //nolint:errcheck
	last := ansi.Strip(m.chatContent())
	if !strings.Contains(last, "+line 29") || strings.Contains(last, "+line 00") {
		t.Fatalf("last detail page = %q", last)
	}
	m.approvalQueue.DecideAll(approval.Decision{Approved: false})
}

func TestModel_Submit_BlockedByQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	session := &fakeSession{}
	q := approval.NewApprovalQueue(ctx, broker)
	m := newModel(ctx, session, nil, q, nil, false, nil, nil)

	done := make(chan struct{})
	go func() {
		_, _ = broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		close(done)
	}()
	waitForQueueLen(t, m.approvalQueue, 1)

	m.input.SetValue("hello")
	m.submit() //nolint:errcheck
	if len(session.calls) != 0 {
		t.Fatalf("expected no session calls while queue is non-empty, got %d", len(session.calls))
	}
	m.approvalQueue.DecideAll(approval.Decision{Approved: true})
	<-done
}

func TestModel_QueueEmpty_SubmitAllowed(t *testing.T) {
	session := &fakeSession{}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)
	m.input.SetValue("hello")

	_, cmd := m.submit()
	if cmd == nil {
		t.Fatal("expected non-nil cmd when queue is empty (submit allowed)")
	}
}

func TestIsToolEvent(t *testing.T) {
	if isToolEvent(events.Event{Type: events.EventToolStarted}) {
		t.Fatal("tool event without payload should be ignored")
	}
	if isToolEvent(events.Event{Type: events.EventAfterResponse, Tool: &events.ToolPayload{Name: "read_file"}}) {
		t.Fatal("non-tool event type should be ignored")
	}
	if !isToolEvent(events.Event{Type: events.EventToolStarted, Tool: &events.ToolPayload{Name: "read_file"}}) {
		t.Fatal("tool started event should be accepted")
	}
	if !isToolEvent(events.Event{Type: events.EventNotice, Notice: &events.NoticePayload{Text: "compacting"}}) {
		t.Fatal("notice event should be accepted")
	}
}

func TestRegisterToolEventsWakesAfterQueueing(t *testing.T) {
	bus := events.NewOrderedDispatcher(nil)
	queue := newToolEventQueue()
	var drained []events.Event
	unregister := registerToolEvents(bus, &fakeSession{}, queue.Push, func() {
		drained = append(drained, queue.Drain()...)
	})
	defer unregister()

	want := events.Event{Type: events.EventNotice, Notice: &events.NoticePayload{Text: "job completed"}}
	bus.Dispatch(context.Background(), want)
	if len(drained) != 1 || drained[0].Notice.Text != want.Notice.Text {
		t.Fatalf("wake drained events = %#v, want queued notice", drained)
	}
}

func TestModel_ChatScrollsWithPageKeys(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.width = 80
	m.height = 8
	m.chatLog.Append(message{role: "assistant", text: strings.Repeat("line\n", 40)})
	m.syncChatViewport(true)
	if !m.chat.Following() {
		t.Fatal("expected chat to start at bottom")
	}

	updated, _ := m.Update(testKey(tea.KeyPgUp))
	got := updated.(*model)
	if got.chat.Following() {
		t.Fatal("expected PgUp to scroll chat away from bottom")
	}

	updated, _ = got.Update(testKey(tea.KeyPgDown))
	got = updated.(*model)
	if !got.chat.Following() {
		t.Fatal("expected PgDown to return chat to bottom")
	}
}

// TestModel_AutoFollowRespectsUserScroll pins the current auto-follow-bottom
// behavior: once the user scrolls away from the bottom, new content (tool
// events, spinner ticks) must not yank the view back down; only returning to
// the bottom (e.g. via End) resumes auto-follow. This guards the scroll
// boundary before chatLog extraction moves message/tool-block state out of
// the model.
func TestModel_AutoFollowRespectsUserScroll(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.width = 80
	m.height = 8
	m.sending = true
	m.chatLog.Append(message{role: "assistant", text: strings.Repeat("line\n", 40)})
	m.syncChatViewport(true)
	if !m.chat.Following() {
		t.Fatal("expected chat to start at bottom")
	}

	// User scrolls away: userScrolled flips true and stays there.
	updated, _ := m.Update(testKey(tea.KeyPgUp))
	got := updated.(*model)
	if got.chat.Following() || !got.userScrolled {
		t.Fatalf("expected scroll-away to set userScrolled, got atBottom=%v userScrolled=%v", got.chat.Following(), got.userScrolled)
	}

	// New tool output arrives while sending; a forced-bottom sync (as done by
	// drainToolEvents/log ticks) must not override the user's scroll position.
	got.chatLog.ApplyEvent(events.Event{Type: events.EventToolStarted, Tool: &events.ToolPayload{ID: "call_1", Name: "shell", Arguments: `{"command":"ls"}`}})
	got.syncChatViewport(true)
	if got.chat.Following() {
		t.Fatal("new content should not pull the view back to bottom while userScrolled is set")
	}

	// Returning to the bottom (End) clears userScrolled and resumes auto-follow.
	updated, _ = got.Update(testKey(tea.KeyEnd))
	got = updated.(*model)
	if !got.chat.Following() || got.userScrolled {
		t.Fatalf("expected End to reach bottom and clear userScrolled, got atBottom=%v userScrolled=%v", got.chat.Following(), got.userScrolled)
	}

	got.chatLog.ApplyEvent(events.Event{Type: events.EventToolCompleted, Tool: &events.ToolPayload{ID: "call_1", Name: "shell", Duration: time.Millisecond}})
	got.syncChatViewport(true)
	if !got.chat.Following() {
		t.Fatal("expected auto-follow to resume once user returned to bottom")
	}
}

func TestModel_LogEnabledStartupShowsChatTabsOnly(t *testing.T) {
	m := newTestModelWithLogs("2026-05-03T00:00:00Z INFO agent request started")
	m.chatLog.Append(message{role: "assistant", text: "chat response"})

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Chat") || !strings.Contains(view, "Logs") {
		t.Fatalf("view missing tabs: %q", view)
	}
	if !strings.Contains(view, "chat response") || strings.Contains(view, "agent request") {
		t.Fatalf("startup view mixed modes: %q", view)
	}
	if m.logPanel.mode != modeChat || !m.input.Focused() {
		t.Fatalf("startup mode = %v, input focused = %v", m.logPanel.mode, m.input.Focused())
	}
}

func TestModel_ViewOmitsTabsWhenLogViewDisabled(t *testing.T) {
	m := newTestModelWithHiddenLogs("2026-05-03T00:00:00Z INFO agent request started")

	view := ansi.Strip(m.View().Content)
	if strings.Contains(view, "Logs") || strings.Contains(view, "switch view") {
		t.Fatalf("view unexpectedly rendered log tabs: %q", view)
	}

	updated, _ := m.Update(testKey(tea.KeyTab))
	got := updated.(*model)
	if got.logPanel.mode != modeChat {
		t.Fatal("expected tab to ignore logs when log view is disabled")
	}
}

func TestModel_TabSwitchesBetweenChatAndLogs(t *testing.T) {
	m := newTestModelWithLogs("log")

	updated, _ := m.Update(testKey(tea.KeyTab))
	got := updated.(*model)
	if got.logPanel.mode != modeLogs {
		t.Fatal("expected Logs mode after tab")
	}
	if got.input.Focused() {
		t.Fatal("input cursor should be blurred in Logs mode")
	}

	updated, _ = got.Update(testKey(tea.KeyTab))
	got = updated.(*model)
	if got.logPanel.mode != modeChat {
		t.Fatal("expected Chat mode after a second tab")
	}
	if !got.input.Focused() {
		t.Fatal("input cursor should be focused in Chat mode")
	}
}

func TestModel_LogsRenderFullWidthAtNarrowSize(t *testing.T) {
	line := strings.Repeat("a", 79) + "BCDEF"
	m := newTestModelWithLogs(line)
	m.width = 80
	m.showLogs()

	view := ansi.Strip(m.logsView())
	if m.logPanel.viewport.Width() != 80 {
		t.Fatalf("viewport width = %d, want 80", m.logPanel.viewport.Width())
	}
	if strings.Contains(view, "...") {
		t.Fatalf("logs view cropped line: %q", view)
	}
	unwrapped := strings.ReplaceAll(strings.ReplaceAll(ansi.Strip(m.logPanel.viewport.View()), "\n", ""), " ", "")
	if !strings.Contains(unwrapped, line) {
		t.Fatalf("logs view did not wrap full line: %q", view)
	}
}

func TestModel_LogsScrollWithKeysAndMouse(t *testing.T) {
	m := newTestModelWithLogs("one", "two", "three", "four", "five", "six", "seven", "eight")
	m.height = 8
	m.showLogs()

	updated, _ := m.Update(testKey(tea.KeyUp))
	got := updated.(*model)
	if got.logPanel.viewport.AtBottom() {
		t.Fatal("expected logs to scroll away from the bottom after up")
	}

	view := got.logsView()
	if !strings.Contains(view, "three") || strings.Contains(view, "eight") {
		t.Fatalf("logs view did not scroll independently: %q", view)
	}

	updated, _ = got.Update(testKey(tea.KeyEnd))
	got = updated.(*model)
	if !got.logPanel.viewport.AtBottom() {
		t.Fatal("expected logs to return to the bottom after End")
	}

	updated, _ = got.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	got = updated.(*model)
	if got.logPanel.viewport.AtBottom() {
		t.Fatal("expected mouse wheel to scroll active Logs view")
	}
}

func TestHandlePlainLocalCommand_ModelSwitch(t *testing.T) {
	session := &fakeSession{model: "old-model"}
	var out bytes.Buffer
	handled, exit := handlePlainLocalCommand(&out, session, "/model new-model")
	if !handled || exit {
		t.Fatalf("expected handled=true exit=false, got handled=%v exit=%v", handled, exit)
	}
	if session.model != "new-model" {
		t.Fatalf("expected model=new-model, got %q", session.model)
	}
	if !strings.Contains(out.String(), "model set to new-model") {
		t.Fatalf("output missing confirmation: %q", out.String())
	}
}

func TestHandlePlainLocalCommand_ModelList(t *testing.T) {
	session := &fakeSession{model: "current", models: []string{"current", "other"}}
	var out bytes.Buffer
	handled, exit := handlePlainLocalCommand(&out, session, "/model")
	if !handled || exit {
		t.Fatalf("expected handled=true exit=false, got handled=%v exit=%v", handled, exit)
	}
	outStr := out.String()
	if !strings.Contains(outStr, "← current") {
		t.Fatalf("output missing current marker: %q", outStr)
	}
	if !strings.Contains(outStr, "other") {
		t.Fatalf("output missing other model: %q", outStr)
	}
}

func TestModel_SubmitModelSwitchSetsModel(t *testing.T) {
	session := &fakeSession{model: "old"}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)
	m.input.SetValue("/model new-model")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	got := updated.(*model)
	if cmd != nil {
		t.Fatalf("expected nil cmd, got non-nil")
	}
	if session.model != "new-model" {
		t.Fatalf("expected model=new-model, got %q", session.model)
	}
	if len(got.chatLog.messages) != 1 || !strings.Contains(got.chatLog.messages[0].text, "new-model") {
		t.Fatalf("messages = %#v", got.chatLog.messages)
	}
}

func TestModel_SubmitModelListReturnsCmd(t *testing.T) {
	session := &fakeSession{model: "m1", models: []string{"m1", "m2"}}
	m := newModel(context.Background(), session, nil, nil, nil, false, nil, nil)
	m.input.SetValue("/model")

	_, cmd := m.Update(testKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected async cmd for /model bare, got nil")
	}
	msg := cmd()
	listMsg, ok := msg.(modelListMsg)
	if !ok {
		t.Fatalf("expected modelListMsg, got %T", msg)
	}
	if listMsg.current != "m1" {
		t.Fatalf("expected current=m1, got %q", listMsg.current)
	}
	if len(listMsg.models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(listMsg.models))
	}
}

func TestModel_LogsModeOwnsPageKeys(t *testing.T) {
	m := newTestModelWithLogs("one", "two", "three", "four", "five", "six", "seven", "eight")
	m.height = 8
	m.chatLog.Append(message{role: "assistant", text: strings.Repeat("chat\n", 40)})
	m.syncChatViewport(true)
	m.showLogs()
	if !m.chat.Following() || !m.logPanel.viewport.AtBottom() {
		t.Fatal("test setup expected chat and logs at bottom")
	}

	updated, _ := m.Update(testKey(tea.KeyPgUp))
	got := updated.(*model)
	if !got.chat.Following() {
		t.Fatal("expected focused logs to own PgUp without moving chat")
	}
	if got.logPanel.viewport.AtBottom() {
		t.Fatal("expected focused logs to scroll on PgUp")
	}

	updated, _ = got.Update(testKey(tea.KeyPgDown))
	got = updated.(*model)
	if !got.logPanel.viewport.AtBottom() {
		t.Fatal("expected focused logs to return bottom on PgDown")
	}
}

func TestModel_LogsResizePreservesModeAndScroll(t *testing.T) {
	lines := strings.Split(strings.Repeat("log line\n", 60), "\n")
	m := newTestModelWithLogs(lines...)
	m.height = 10
	m.showLogs()
	m.updateKey(testKey(tea.KeyPgUp)) //nolint:errcheck
	if m.logPanel.viewport.AtBottom() {
		t.Fatal("test setup expected Logs scrolled away from bottom")
	}

	for _, width := range []int{140, 80, 140} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 10}) //nolint:errcheck
		if m.logPanel.mode != modeLogs || m.logPanel.viewport.Width() != width {
			t.Fatalf("resize %d: mode=%v viewport width=%d", width, m.logPanel.mode, m.logPanel.viewport.Width())
		}
		if m.logPanel.viewport.AtBottom() {
			t.Fatalf("resize %d moved Logs to bottom", width)
		}
	}
}

func TestModel_LogsAutoFollowRespectsScroll(t *testing.T) {
	source := &fakeLogSource{lines: strings.Split(strings.Repeat("log line\n", 40), "\n")}
	m := newModel(context.Background(), &fakeSession{}, nil, nil, source, true, nil, nil)
	m.width, m.height = 80, 10
	m.showLogs()
	if !m.logPanel.viewport.AtBottom() {
		t.Fatal("expected Logs to start at bottom")
	}

	source.lines = append(source.lines, "new bottom line")
	m.syncLogViewport()
	if !m.logPanel.viewport.AtBottom() {
		t.Fatal("expected Logs at bottom to follow new lines")
	}

	m.updateKey(testKey(tea.KeyPgUp)) //nolint:errcheck
	source.lines = append(source.lines, "do not follow")
	m.syncLogViewport()
	if m.logPanel.viewport.AtBottom() {
		t.Fatal("new lines should not move a scrolled Logs viewport")
	}
}

// While an approval dialog is open it owns every key but ctrl+c: the input box
// is locked and rendered dim, so typing mid-turn can neither decide a request
// nor land in a prompt nobody is listening to. Closing the dialog restores it.
func TestModel_ApprovalOpenLocksTheInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)
	defer queue.Close()
	m := newModel(ctx, &fakeSession{}, nil, queue, nil, false, nil, nil)
	m.width, m.height = 80, 24

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()
	waitForQueueLen(t, queue, 1)

	// A word with a decision letter in it must decide nothing and type nothing:
	// the box stays empty and says where the keys go.
	for _, ch := range []string{"a", "b", "c", " ", "x"} {
		m.updateKey(testTextKey(ch)) //nolint:errcheck
	}
	if queue.Len() != 1 {
		t.Fatal("typing must not decide a pending approval")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input = %q, want empty while an approval is open", got)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "answer the approval above") {
		t.Fatalf("locked input placeholder missing from %q", view)
	}

	// The dialog's own keys still work, including the one whose letter also
	// appears in the word the user tried to type.
	m.updateKey(testTextKey("v")) //nolint:errcheck
	if !m.approvals.expanded {
		t.Fatal("v must still expand the selected approval")
	}
	m.updateKey(testTextKey("n")) //nolint:errcheck
	if err := <-done; err == nil {
		t.Fatal("n should still deny the selected request")
	}
	// The dialog closed: the prompt is a prompt again, not a locked note.
	if view = ansi.Strip(m.View().Content); strings.Contains(view, "answer the approval above") {
		t.Fatal("closed approval dialog must restore the input box")
	}
}

// A paste is no exception to the lock: it is not a key, so the key routing alone
// would leave it typing into the box the dialog owns. While an approval is open
// the pasted text must stay out of the draft, and it must not decide the request.
func TestModel_ApprovalOpenIgnoresPaste(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)
	defer queue.Close()
	m := newModel(ctx, &fakeSession{}, nil, queue, nil, false, nil, nil)
	m.width, m.height = 80, 24

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()
	waitForQueueLen(t, queue, 1)

	// A paste that reads like a decision letter must neither type nor decide: the
	// box stays empty and the request is still pending.
	_, _ = m.Update(tea.PasteMsg{Content: "y\n"})
	if got := m.input.Value(); got != "" || len(m.images) != 0 || queue.Len() != 1 {
		t.Fatalf("paste while an approval is open: input = %q, images = %d, pending = %d", got, len(m.images), queue.Len())
	}
	// A plain paste is held back the same way, so the locked note still shows.
	_, _ = m.Update(tea.PasteMsg{Content: "ls -l"})
	if got := m.input.Value(); got != "" || !strings.Contains(ansi.Strip(m.View().Content), "answer the approval above") {
		t.Fatalf("paste while an approval is open: input = %q, want the locked note", got)
	}
	// The dialog still answers its own key once the pasted text is gone.
	m.updateKey(testTextKey("n")) //nolint:errcheck
	if err := <-done; err == nil {
		t.Fatal("n should still deny the selected request")
	}
}

func TestModel_PendingApprovalClearIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)
	defer queue.Close()
	m := newModel(ctx, &fakeSession{}, nil, queue, nil, false, nil, nil)
	m.width, m.height = 80, 24

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()
	waitForQueueLen(t, queue, 1)
	defer func() {
		queue.DecideAll(approval.Decision{Approved: false, Reason: "test cleanup"})
		<-done
	}()

	m.input.SetValue("/clear")
	m.updateKey(testKey(tea.KeyEnter)) //nolint:errcheck
	last := m.chatLog.Last()
	if last == nil || !strings.Contains(last.text, "approvals pending") {
		t.Fatalf("expected refusal notice, got %+v", last)
	}
	if queue.Len() != 1 {
		t.Fatal("/clear must not decide a pending approval")
	}
}

func TestModel_PendingApprovalDefersToChat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := approval.NewBroker()
	queue := approval.NewApprovalQueue(ctx, broker)
	defer queue.Close()
	m := newModel(ctx, &fakeSession{}, nil, queue, fakeLogSource{lines: []string{"log"}}, true, nil, nil)
	m.width, m.height = 80, 24
	m.showLogs()

	done := make(chan error, 1)
	go func() {
		_, err := broker.RequestApproval(ctx, approval.Request{Tool: "shell"})
		done <- err
	}()
	waitForQueueLen(t, queue, 1)

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Chat •") || strings.Contains(view, "APPROVAL") {
		t.Fatalf("pending approval indicator/render = %q", view)
	}
	m.updateKey(testTextKey("y")) //nolint:errcheck
	if queue.Len() != 1 {
		t.Fatal("hidden approval action should not be accepted")
	}
	m.updateKey(testKey(tea.KeyTab)) //nolint:errcheck
	if m.logPanel.mode != modeChat || !strings.Contains(ansi.Strip(m.View().Content), "APPROVAL") {
		t.Fatal("Tab should reveal pending approval in Chat")
	}
	m.updateKey(testKey(tea.KeyTab)) //nolint:errcheck
	if m.logPanel.mode != modeChat {
		t.Fatal("pending approval should lock view switching in Chat")
	}
	m.updateKey(testTextKey("y")) //nolint:errcheck
	if err := <-done; err != nil {
		t.Fatalf("approve pending request: %v", err)
	}
}

func TestHelpDismissalCollapsesTheKeyBlock(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

	m.updateKey(testModifiedKey('h', tea.ModCtrl)) //nolint:errcheck
	if !m.help.ShowAll || !m.overlay.IsOpen() {
		t.Fatalf("ctrl+h did not open help: showAll=%v open=%v", m.help.ShowAll, m.overlay.IsOpen())
	}

	m.updateKey(testKey(tea.KeyEscape)) //nolint:errcheck
	if m.overlay.IsOpen() {
		t.Fatal("esc did not dismiss the help popup")
	}
	if m.help.ShowAll {
		t.Fatalf("footer kept the expanded key block after dismissal: %q", ansi.Strip(m.footer()))
	}
}

func TestStatuslineSitsUnderTheInput(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.statusline = statusline{folder: "mercury", branch: "dev"}

	lines := strings.Split(strings.TrimRight(ansi.Strip(m.mainView()), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "mercury") || !strings.Contains(last, "dev") {
		t.Fatalf("statusline is not the bottom row: %q", last)
	}
	t.Logf("statusline: %q", last)
}

func TestStatuslineShortPath(t *testing.T) {
	for dir, want := range map[string]string{
		"/home/fr0sty/GolandProjects/mercury": "GolandProjects/mercury",
		"/home/fr0sty":                        "home/fr0sty",
		"/tmp":                                "tmp",
		"":                                    "",
	} {
		if got := shortPath(dir); got != want {
			t.Fatalf("shortPath(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestStatuslineDropsCellsThatDoNotFit(t *testing.T) {
	s := statusline{folder: "GolandProjects/mercury", branch: "dev"}
	if got := ansi.Strip(s.view("qwen3-coder", "", "", 20)); strings.Contains(got, "qwen3-coder") {
		t.Fatalf("narrow statusline kept the model cell: %q", got)
	}
	if got := ansi.Strip(s.view("qwen3-coder", "", "", 120)); !strings.Contains(got, "qwen3-coder") {
		t.Fatalf("wide statusline dropped the model cell: %q", got)
	}
}

func TestMouseModeMapsToTrackingMode(t *testing.T) {
	for name, want := range map[string]tea.MouseMode{
		"":    tea.MouseModeCellMotion,
		"on":  tea.MouseModeCellMotion,
		"off": tea.MouseModeNone,
		"all": tea.MouseModeAllMotion,
	} {
		mode, ok := parseMouseMode(name)
		if !ok {
			t.Fatalf("parseMouseMode(%q) rejected a valid preset", name)
		}
		if got := mode.teaMode(); got != want {
			t.Fatalf("mode %q = %v, want %v", name, got, want)
		}
	}
	if _, ok := parseMouseMode("wheel"); ok {
		t.Fatal("parseMouseMode accepted an unknown preset")
	}
}

func TestMouseCommandPersistsThePreset(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.prefsPath = filepath.Join(t.TempDir(), prefsFile)

	m.setMouseMode([]string{"off"})
	if m.mouseMode != mouseOff {
		t.Fatalf("mouse mode = %v, want off", m.mouseMode)
	}
	if got := loadPrefs(m.prefsPath).MouseMode; got != "off" {
		t.Fatalf("saved mouse mode = %q, want %q", got, "off")
	}

	m.setMouseMode([]string{"nonsense"})
	if m.mouseMode != mouseOff {
		t.Fatalf("a bad preset changed the mode to %v", m.mouseMode)
	}
}

func TestIdlePromptDoesNotScheduleCursorRepaints(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	if _, cmd := m.Update(textarea.Blink()); cmd != nil {
		t.Fatal("idle cursor schedules terminal output that can clear native selection")
	}
}

func TestMouseDragSelectsAndCopies(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.frame = "hello world\nsecond line"

	if _, handled := m.updateMouse(tea.MouseClickMsg{X: 6, Y: 0, Button: tea.MouseLeft}); !handled {
		t.Fatal("click did not start a selection")
	}
	if _, handled := m.updateMouse(tea.MouseMotionMsg{X: 11, Y: 0, Button: tea.MouseLeft}); !handled {
		t.Fatal("drag did not extend the selection")
	}
	if got := ansi.Strip(m.selection.Paint(m.frame, selectionStyle())); got != m.frame {
		t.Fatalf("painted frame text = %q, want %q", got, m.frame)
	}

	cmd, handled := m.updateMouse(tea.MouseReleaseMsg{X: 11, Y: 0, Button: tea.MouseLeft})
	if !handled || cmd == nil {
		t.Fatal("release did not copy the selection")
	}
	// The highlight survives the release, so the user can see what was copied.
	if m.selection.Empty() {
		t.Fatal("release dropped the highlight")
	}
	m.updateKey(testKey('x')) //nolint:errcheck
	if !m.selection.Empty() {
		t.Fatal("typing did not take the highlight down")
	}
}

func TestMouseOffLeavesSelectionToTheTerminal(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	m.mouseMode = mouseOff
	m.frame = "hello world"

	if _, handled := m.updateMouse(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft}); handled {
		t.Fatal("mouse off still consumed a click")
	}
	if !m.selection.Empty() {
		t.Fatal("mouse off still started a selection")
	}
}

func TestCopyNoticeRidesTheFooterUntilTheHighlightGoes(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.frame = "hello world"

	m.updateMouse(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})    //nolint:errcheck
	m.updateMouse(tea.MouseMotionMsg{X: 11, Y: 0, Button: tea.MouseLeft})  //nolint:errcheck
	m.updateMouse(tea.MouseReleaseMsg{X: 11, Y: 0, Button: tea.MouseLeft}) //nolint:errcheck

	if got := ansi.Strip(m.footer()); !strings.Contains(got, "copy requested: 11 chars") || strings.Contains(got, "copied") {
		t.Fatalf("footer = %q, want the copy notice", got)
	}
	m.updateKey(testKey('x')) //nolint:errcheck
	if got := ansi.Strip(m.footer()); strings.Contains(got, "copied") {
		t.Fatalf("copy notice outlived the highlight: %q", got)
	}
}

func TestEscDropsTheHighlightWithoutInterrupting(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.frame = "hello world"
	m.sending = true

	m.updateMouse(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})    //nolint:errcheck
	m.updateMouse(tea.MouseMotionMsg{X: 11, Y: 0, Button: tea.MouseLeft})  //nolint:errcheck
	m.updateMouse(tea.MouseReleaseMsg{X: 11, Y: 0, Button: tea.MouseLeft}) //nolint:errcheck

	m.updateKey(testKey(tea.KeyEscape)) //nolint:errcheck
	if !m.selection.Empty() {
		t.Fatal("esc did not drop the highlight")
	}
	if m.stopping {
		t.Fatal("the esc that cleared the highlight also interrupted the turn")
	}
}

func TestCopyNoticeExpires(t *testing.T) {
	m := newTestModelWithHiddenLogs()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m.frame = "hello world"

	m.updateMouse(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})    //nolint:errcheck
	m.updateMouse(tea.MouseMotionMsg{X: 11, Y: 0, Button: tea.MouseLeft})  //nolint:errcheck
	m.updateMouse(tea.MouseReleaseMsg{X: 11, Y: 0, Button: tea.MouseLeft}) //nolint:errcheck

	m.copiedAt = time.Now().Add(-copyNoticeWindow - time.Second)
	if got := ansi.Strip(m.footer()); strings.Contains(got, "copied") {
		t.Fatalf("stale copy notice still in the footer: %q", got)
	}
	if m.selection.Empty() {
		t.Fatal("the expired notice took the highlight with it")
	}
}

// A subject with no derivable pattern is not offered "always", and a stray
// "a" refuses rather than storing a rule the store would reject.
func TestAlwaysAllowRefusesAPipedSubject(t *testing.T) {
	if _, err := grantPattern(approval.Request{Tool: "shell", Subject: "| sort"}); err == nil {
		t.Fatalf("grantPattern accepted an underivable subject")
	}
}

func TestElideSubjectBreaksAtPipelineBoundariesAndElides(t *testing.T) {
	got := elideSubject("find . -type d | grep -v .git | sort | head", 60, 3)
	want := []string{"find . -type d |", "  grep -v .git |", "… (v for full)"}
	if len(got) != len(want) {
		t.Fatalf("elideSubject lines = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("elideSubject lines = %q, want %q", got, want)
		}
	}
	if lines := elideSubject("git status", 60, 3); len(lines) != 1 || lines[0] != "git status" {
		t.Fatalf("elideSubject(short) = %q, want the command unchanged", lines)
	}
}

func TestApprovalPanelDropsARedundantWhy(t *testing.T) {
	panel := func(summary string) string {
		item := approval.QueueItem{Pending: approval.Pending{Request: approval.Request{Tool: "shell", Summary: summary, Subject: "git status"}}}
		return ansi.Strip(renderApprovalQueuePanel([]approval.QueueItem{item}, 0, false, 0, defaultKeyMap(false), help.New(), 60))
	}
	if got := panel("Run git status"); strings.Contains(got, "Run git status") {
		t.Fatalf("panel repeats the subject as its why:\n%s", got)
	}
	if got := panel("Run git status"); strings.Contains(got, "yes all") {
		t.Fatalf("a single request offered yes-to-all:\n%s", got)
	}
	if got := panel("list the repo layout"); !strings.Contains(got, "list the repo layout") {
		t.Fatalf("panel dropped a distinct why:\n%s", got)
	}
}
