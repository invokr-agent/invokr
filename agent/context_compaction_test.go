package agent

import (
	"context"
	"errors"
	"github.com/invokr-ai/invokr/capability/agentprompt"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

type compactionFlowGateway struct {
	outputs      []string
	next         int
	summaryErr   error
	summaries    []string
	summaryCalls int
	requests     [][]inference.Message
}

type blockingPrefetchGateway struct{ canceled chan error }

func (*blockingPrefetchGateway) Begin(context.Context, BeginModelRequest) (ModelTurn, error) {
	return ModelTurn{}, nil
}
func (*blockingPrefetchGateway) Continue(context.Context, ContinueModelRequest) (ModelTurn, error) {
	return ModelTurn{}, nil
}
func (g *blockingPrefetchGateway) SummarizeConversation(ctx context.Context, _ []inference.Message) (string, error) {
	<-ctx.Done()
	g.canceled <- ctx.Err()
	return "", ctx.Err()
}

func TestSummaryPrefetchUsesSessionBackgroundContext(t *testing.T) {
	background, cancel := context.WithCancel(context.Background())
	gateway := &blockingPrefetchGateway{canceled: make(chan error, 1)}
	budget := &contextBudget{gateway: gateway, backgroundContext: background, logger: zap.NewNop()}
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("start"), inference.AssistantMessage("one"),
		inference.UserMessage("two"), inference.AssistantMessage("three"), inference.UserMessage("four"),
		inference.AssistantMessage("five"),
	}
	budget.prefetchSummary(messages, 1, 1<<20)
	select {
	case err := <-gateway.canceled:
		t.Fatalf("prefetch canceled before session shutdown: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-gateway.canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("prefetch error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("prefetch was not canceled by session shutdown")
	}
}

func TestContextBudgetUsesConfiguredWarningThreshold(t *testing.T) {
	budget := &contextBudget{
		windowSize: 100, warningPercent: 50, compactPercent: 90,
		events: events.NewOrderedDispatcher(zap.NewNop()), logger: zap.NewNop(),
	}
	messages := []inference.Message{inference.UserMessage(strings.Repeat("x", 220))}
	_, state := budget.Prepare(context.Background(), "s1", messages, contextWindowState{})
	if !state.warningSent {
		t.Fatal("configured 50% warning threshold was not applied")
	}
}

// At the warning threshold the headroom left in the window is smaller than the
// summary's own completion cap: no prefetch, since running it beside the agent
// turn overflows a unified KV cache and evicts the conversation's prefix (#577).
func TestWarningSkipsPrefetchWithoutRoomForTheSummaryCompletion(t *testing.T) {
	gateway := &blockingPrefetchGateway{canceled: make(chan error, 1)}
	budget := &contextBudget{
		gateway: gateway, backgroundContext: context.Background(),
		windowSize: 64000, warningPercent: 50, compactPercent: 99,
		events: events.NewOrderedDispatcher(zap.NewNop()), logger: zap.NewNop(),
	}
	// ≈ 61k tokens: past the warning, under compaction, ~3k headroom < 4096 cap.
	messages := []inference.Message{inference.SystemMessage("system"), inference.UserMessage("start")}
	for range 60 {
		messages = append(messages, inference.AssistantMessage(strings.Repeat("x", 4000)))
	}
	_, state := budget.Prepare(context.Background(), "s1", messages, contextWindowState{})
	if !state.warningSent {
		t.Fatal("fixture did not cross the warning threshold")
	}
	if budget.summaryFuture != nil {
		t.Fatal("prefetch launched without room for the summary completion")
	}
}

func (g *compactionFlowGateway) Begin(context.Context, BeginModelRequest) (ModelTurn, error) {
	return g.turn([]inference.Message{inference.SystemMessage("system"), inference.UserMessage("original request")})
}

func (g *compactionFlowGateway) Continue(_ context.Context, req ContinueModelRequest) (ModelTurn, error) {
	g.requests = append(g.requests, append([]inference.Message(nil), req.Messages...))
	return g.turn(req.Messages)
}

func (g *compactionFlowGateway) turn(messages []inference.Message) (ModelTurn, error) {
	if g.next >= len(g.outputs) {
		return ModelTurn{}, errors.New("no model turn")
	}
	output := g.outputs[g.next]
	g.next++
	return ModelTurn{Messages: messages, Completion: completionOutput{content: output}}, nil
}

func (g *compactionFlowGateway) SummarizeConversation(context.Context, []inference.Message) (string, error) {
	g.summaryCalls++
	if g.summaryErr != nil {
		return "", g.summaryErr
	}
	if len(g.summaries) >= g.summaryCalls {
		return g.summaries[g.summaryCalls-1], nil
	}
	return "old tool work summarized", nil
}

// A failed prefetch, even one covering only a prefix of the middle, falls back
// to summarizing the plan's whole middle now.
func TestSummarizeMessagesRetriesFailedPrefetch(t *testing.T) {
	gateway := &compactionFlowGateway{summaries: []string{"recovered summary"}}
	budget := &contextBudget{gateway: gateway}
	messages := replanMessages(10)
	plan := planRetention(messages, 0, 1, nil)
	budget.summaryFuture = &summaryFuture{key: summaryKey(messages[2:5]), end: 5, done: make(chan struct{}), err: errors.New("prefetch canceled")}
	close(budget.summaryFuture.done)

	compacted, err := budget.summarizeMessages(context.Background(), messages, plan)
	if err != nil {
		t.Fatal(err)
	}
	if gateway.summaryCalls != 1 || !strings.Contains(compacted[2].Content, "recovered summary") || len(compacted) != 3+len(messages)-plan.tailStart {
		t.Fatalf("calls = %d, compacted = %#v", gateway.summaryCalls, compacted)
	}
}

func TestRunnerCompactsContextAndCompletes(t *testing.T) {
	gateway, runner := compactionTestRunner(t, nil)
	runner.contextState = contextWindowState{warningSent: true, providerPromptTokens: 100, providerMessageTokens: 80}
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != "done" || !requestsContainMessage(gateway.requests, conversationSummaryLabel) {
		t.Fatalf("response = %#v, requests = %#v", resp, gateway.requests)
	}
	request := requestContaining(gateway.requests, conversationSummaryLabel)
	if request[0].Content != "system" || request[1].Content != "original request" {
		t.Fatalf("original messages changed: %#v", request[:2])
	}
	if request[2].Role != inference.RoleAssistant {
		t.Fatalf("conversation summary must be assistant-derived context, got %#v", request[2])
	}
	if runner.contextState.providerPromptTokens != 0 || runner.contextState.providerMessageTokens != 0 {
		t.Fatalf("provider token anchor survived summarization: %#v", runner.contextState)
	}
}

func TestRunnerElidesToolOutputWhenCompactionFails(t *testing.T) {
	gateway, runner := compactionTestRunner(t, errors.New("summarizer unavailable"))
	runner.contextState = contextWindowState{warningSent: true, providerPromptTokens: 100, providerMessageTokens: 80}
	resp, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != "done" || !requestsContainMessage(gateway.requests, "output elided to fit context") {
		t.Fatalf("response = %#v, requests = %#v", resp, gateway.requests)
	}
	if runner.contextState.providerPromptTokens != 0 || runner.contextState.providerMessageTokens != 0 {
		t.Fatalf("provider token anchor survived elision: %#v", runner.contextState)
	}
}

// A failed summarization must still reduce the context. The shape that broke
// these runs is one enormous tool result inside the retained tail: the middle
// passes have nothing left to elide, so without the tail pass the transcript
// stays over the window and the run continues past 100%.
func TestCompactionReducesContextWhenSummarizationFails(t *testing.T) {
	huge := strings.Repeat("y", 40000)
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		inference.AssistantMessage("", inference.ToolCall{ID: "call-1", Name: "read_file"}),
		inference.ToolMessage("call-1", huge),
		inference.AssistantMessage("", inference.ToolCall{ID: "call-2", Name: "read_file"}),
		inference.ToolMessage("call-2", huge),
		inference.UserMessage("continue"), inference.AssistantMessage("more"),
	}
	budget := &contextBudget{
		windowSize: 8000, gateway: &compactionFlowGateway{summaryErr: errors.New("summarizer unavailable")},
		events: events.NewOrderedDispatcher(zap.NewNop()), logger: zap.NewNop(),
	}
	state := contextWindowState{tokenEstimateScale: 1}
	before := estimatedMessageTokens(messages)
	compacted, _ := budget.Prepare(context.Background(), "s1", messages, state)
	after := estimatedMessageTokens(compacted)
	if after >= before {
		t.Fatalf("context not reduced: %d -> %d", before, after)
	}
	if limit := budget.windowSize * int64(budget.compactThreshold()) / 100; after > limit {
		t.Fatalf("context still over the compaction budget: %d tokens against %d", after, limit)
	}
}

func TestContextEstimateAndFallbackIncludeNativeToolArguments(t *testing.T) {
	arguments := `{"path":"` + strings.Repeat("x", 2000) + `"}`
	message := (OpenAINativeToolCodec{}).AssistantMessage("", []inference.ToolCall{{
		ID:        "call-1",
		Name:      "read_file",
		Arguments: arguments,
	}})
	messages := []inference.Message{
		inference.SystemMessage("system"),
		inference.UserMessage("read a file"),
		message,
		inference.ToolMessage("call-1", "failed"),
		inference.UserMessage("next"),
		inference.AssistantMessage("working"),
		inference.UserMessage("continue"),
		inference.AssistantMessage("more"),
		inference.UserMessage("again"),
	}
	if got := estimatedMessageTokens(messages); got < 400 {
		t.Fatalf("native tool arguments missing from estimate: %d", got)
	}

	compacted, _ := elideOldToolOutput(messages, planRetention(messages, 0, 1, nil), 200)
	got := compacted[2].ToolCalls[0].Arguments
	if !strings.Contains(got, "removed to fit context") {
		t.Fatalf("tool arguments not elided: %q", got)
	}
	if original := messages[2].ToolCalls[0].Arguments; original != arguments {
		t.Fatal("input messages mutated")
	}
}

func TestContextStateAfterHistoryRewrite(t *testing.T) {
	tests := []struct {
		name        string
		messages    []inference.Message
		window      int64
		wantWarning bool
	}{
		{name: "below warning threshold resets warning", messages: []inference.Message{inference.UserMessage("short")}, window: 100, wantWarning: false},
		{name: "above warning threshold keeps warning", messages: []inference.Message{inference.UserMessage(strings.Repeat("x", 80))}, window: 20, wantWarning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := (contextWindowState{warningSent: true, providerPromptTokens: 99, providerMessageTokens: 88}).afterRewrite(tt.messages, tt.window, constants.ContextWarningPercent)
			if state.providerPromptTokens != 0 || state.providerMessageTokens != 0 || state.warningSent != tt.wantWarning {
				t.Fatalf("state after rewrite = %#v", state)
			}
		})
	}
}

func TestRequestedCompactionBypassesThreshold(t *testing.T) {
	gateway, runner := compactionTestRunner(t, nil)
	runner.budget.windowSize = 1_000_000
	runner.budget.forceCompaction.Store(true)
	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"}); err != nil {
		t.Fatal(err)
	}
	if !requestsContainMessage(gateway.requests, conversationSummaryLabel) {
		t.Fatal("requested compaction did not summarize context")
	}
}

func compactionTestRunner(t *testing.T, summaryErr error) (*compactionFlowGateway, *Runner) {
	t.Helper()
	toolCall := `<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`
	gateway := &compactionFlowGateway{outputs: []string{toolCall, toolCall, toolCall, `{"type":"reply","message":"done"}`}, summaryErr: summaryErr}
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "demo", Metadata: core.ToolMetadata{AlwaysVisible: true}}, func(context.Context, *core.ToolCall) (string, error) {
		return strings.Repeat("tool-output-", 20), nil
	})
	bus := events.NewOrderedDispatcher(zap.NewNop())
	return gateway, &Runner{
		modelGateway:  gateway,
		turnProcessor: &TurnProcessor{events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: caller, logger: zap.NewNop()},
		events:        bus, settings: config.AgentConfig{MaxTurns: 4}, logger: zap.NewNop(), usage: turn.NewSessionTokenUsage(40),
		budget: &contextBudget{windowSize: 40, gateway: gateway, events: bus, logger: zap.NewNop()},
	}
}

func requestsContainMessage(requests [][]inference.Message, needle string) bool {
	return requestContaining(requests, needle) != nil
}

func requestContaining(requests [][]inference.Message, needle string) []inference.Message {
	for _, request := range requests {
		for _, message := range request {
			if strings.Contains(message.Content, needle) {
				return request
			}
		}
	}
	return nil
}

func TestSummarizeMessagesKeepsSummaryAsAssistantMessage(t *testing.T) {
	// Some provider templates allow a system message only at position zero, so
	// the summary stays outside system and must not claim user authority.
	budget := &contextBudget{gateway: &compactionFlowGateway{summaries: []string{"a summary"}}}
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		inference.AssistantMessage("one"), inference.UserMessage("two"),
		inference.AssistantMessage("three"), inference.UserMessage("four"),
		inference.AssistantMessage("five"), inference.UserMessage("six"),
	}
	compacted, err := budget.summarizeMessages(context.Background(), messages, planRetention(messages, 0, 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	if compacted[2].Role != inference.RoleAssistant {
		t.Fatalf("summary role = %q, want assistant", compacted[2].Role)
	}
	if !strings.Contains(compacted[2].Content, "a summary") {
		t.Fatalf("summary content = %q", compacted[2].Content)
	}
}

func TestSummarizeMessagesEmitsNoOrphanedToolMessages(t *testing.T) {
	budget := &contextBudget{gateway: &compactionFlowGateway{summaries: []string{"a summary"}}}
	call := func(id string) inference.Message {
		return inference.AssistantMessage("", inference.ToolCall{ID: id, Name: "read_file", Arguments: `{}`})
	}
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		call("a"), inference.ToolMessage("a", "one"), inference.UserMessage("next"),
		call("b"), inference.ToolMessage("b", "two"),
		call("c"), inference.ToolMessage("c", "three"),
	}
	compacted, err := budget.summarizeMessages(context.Background(), messages, planRetention(messages, 0, 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	if orphans := orphanedToolCallIDs(compacted); len(orphans) > 0 {
		t.Fatalf("compacted transcript orphans %v", orphans)
	}
}

func TestSummarizeMessagesListsReadFiles(t *testing.T) {
	read := func(id, path string) inference.Message {
		return inference.AssistantMessage("", inference.ToolCall{ID: id, Name: readFileToolName, Arguments: `{"path":"` + path + `"}`})
	}
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		read("a", "README.md"), inference.ToolMessage("a", "one"),
		read("b", "docs/RELEASE.md"), inference.ToolMessage("b", strings.Repeat("x", 4096)),
		read("c", "docs/CHANGELOG.md"), inference.ToolMessage("c", strings.Repeat("y", 4096)),
		read("d", "README.md"), inference.ToolMessage("d", "three"),
		inference.AssistantMessage("done"), inference.UserMessage("more"),
		inference.AssistantMessage("tail"), inference.UserMessage("tail"),
	}
	budget := &contextBudget{gateway: &compactionFlowGateway{summaries: []string{"a summary"}}}
	compacted, err := budget.summarizeMessages(context.Background(), messages, planRetention(messages, 0, 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	// Every read in the segment is summarized away, so all three paths are
	// listed once each, in sorted order.
	if notice := compacted[2].Content; !strings.HasSuffix(notice, summaryFilesPrefix+`{"modified":[],"read":["README.md","docs/CHANGELOG.md","docs/RELEASE.md"]}`) {
		t.Fatalf("summary message = %q, want sorted, deduplicated read paths", notice)
	}
	if notice := summaryFiles(messages[9:], nil); notice != "" {
		t.Fatalf("notice for a segment without reads = %q, want empty", notice)
	}
}

func isElisionStub(content string) bool {
	return content == elidedToolOutput || strings.HasSuffix(content, "of output elided to fit context]")
}

func TestElideOldToolOutputLeavesTailByteIdentical(t *testing.T) {
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		inference.ToolMessage("call-1", strings.Repeat("old ", 500)),
		inference.UserMessage("next"),
		inference.ToolMessage("call-2", strings.Repeat("new ", 500)),
		inference.AssistantMessage("working"), inference.UserMessage("continue"),
		inference.AssistantMessage("more"), inference.UserMessage("again"),
	}
	plan := planRetention(messages, 0, 1, nil)
	compacted, _ := elideOldToolOutput(messages, plan, 1)
	for index := plan.tailStart; index < len(messages); index++ {
		if compacted[index].Content != messages[index].Content {
			t.Fatalf("tail message %d rewritten: %q", index, compacted[index].Content)
		}
	}
	if !isElisionStub(compacted[2].Content) {
		t.Fatalf("old tool output not elided: %q", compacted[2].Content)
	}
}

func TestElideOldToolOutputPreservesPinnedResults(t *testing.T) {
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		inference.ToolMessage("edit-1", strings.Repeat("patched ", 300)),
		inference.ToolMessage("read-1", strings.Repeat("contents ", 300)),
		inference.AssistantMessage("a"), inference.UserMessage("b"),
		inference.AssistantMessage("c"), inference.UserMessage("d"),
	}
	plan := planRetention(messages, 0, 1, nil)
	plan.pinned = map[int]bool{2: true}
	compacted, _ := elideOldToolOutput(messages, plan, 1)
	if compacted[2].Content != messages[2].Content {
		t.Fatalf("pinned result rewritten: %q", compacted[2].Content)
	}
	if !isElisionStub(compacted[3].Content) {
		t.Fatalf("unpinned result survived: %q", compacted[3].Content)
	}
}

// M01.3: the compaction trigger measures calibrated context (system prompt and
// tool schemas included) while elidePass measures raw message tokens. The
// fallback budget must be expressed in the estimator's basis or it elides
// nothing.
func TestElisionBudgetSubtractsNonMessageOverhead(t *testing.T) {
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		inference.ToolMessage("call-1", strings.Repeat("old ", 500)),
		inference.AssistantMessage("working"), inference.UserMessage("continue"),
		inference.AssistantMessage("more"), inference.UserMessage("again"),
	}
	// Provider says the whole prompt is 22,300 tokens while the messages alone
	// estimate far less: the difference is system prompt plus tool schemas.
	raw := estimatedMessageTokens(messages)
	state := contextWindowState{
		tokenEstimateScale:    1,
		providerPromptTokens:  22300,
		providerMessageTokens: raw,
	}
	budget := elisionBudget(messages, state, 24000, 85)
	if budget >= raw {
		t.Fatalf("budget %d does not force elision of a %d-token transcript", budget, raw)
	}
	plan := planRetention(messages, 0, 1, nil)
	compacted, notice := elideOldToolOutput(messages, plan, budget)
	if !strings.Contains(notice, "elided old tool output") || !isElisionStub(compacted[2].Content) {
		t.Fatalf("notice = %q, message = %q", notice, compacted[2].Content)
	}
}

func TestElideOldToolOutputReportsNothingElided(t *testing.T) {
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		inference.AssistantMessage("one"), inference.UserMessage("two"),
		inference.AssistantMessage("three"), inference.UserMessage("four"),
		inference.AssistantMessage("five"), inference.UserMessage("six"),
	}
	if _, notice := elideOldToolOutput(messages, planRetention(messages, 0, 1, nil), 1); !strings.Contains(notice, "could not be reduced") {
		t.Fatalf("notice = %q, want no claim of elision", notice)
	}
}

// M02.3: pinning was honoured only by the elision fallback, so the normal
// summarization path dropped the newest write result (ADR 0013).
func TestSummarizeMessagesRetainsPinnedWriteResults(t *testing.T) {
	budget := &contextBudget{gateway: &compactionFlowGateway{summaries: []string{"a summary"}}}
	call := func(id, name string) inference.Message {
		return inference.AssistantMessage("", inference.ToolCall{ID: id, Name: name, Arguments: `{"path":"notes.md"}`})
	}
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("request"),
		call("a", "apply_patch"), inference.ToolMessage("a", "PINNED-CANARY-77"),
		call("b", "read_file"), inference.ToolMessage("b", "contents"),
		inference.AssistantMessage("working"), inference.UserMessage("next"),
		inference.AssistantMessage("more"), inference.UserMessage("again"),
	}
	writes := func(name string) bool { return name == "apply_patch" }
	plan := planRetention(messages, 0, 1, writes)
	if len(plan.pinned) != 1 {
		t.Fatalf("expected one pinned result, got %v", plan.pinned)
	}
	compacted, err := budget.summarizeMessages(context.Background(), messages, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !messagesContain(compacted, "PINNED-CANARY-77") {
		t.Fatalf("pinned write result dropped: %#v", compacted)
	}
	// Reads are summarized away rather than carried verbatim: carrying them
	// raised the post-compaction floor and doubled the compaction rate (#559).
	if messagesContain(compacted, "contents") {
		t.Fatalf("read result carried through summarization: %#v", compacted)
	}
	if orphans := orphanedToolCallIDs(compacted); len(orphans) > 0 {
		t.Fatalf("pinned retention orphaned %v", orphans)
	}
	// The other direction of the pairing invariant: the retained assistant
	// message must not advertise calls whose results were summarized away.
	results := make(map[string]bool)
	for _, message := range compacted {
		results[message.ToolCallID] = true
	}
	for _, message := range compacted {
		for _, toolCall := range message.ToolCalls {
			if !results[toolCall.ID] {
				t.Fatalf("retained tool call %s whose result was dropped", toolCall.ID)
			}
		}
	}
}

func messagesContain(messages []inference.Message, want string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, want) {
			return true
		}
	}
	return false
}

// Bug #2: an assistant message whose payload is ToolCalls rendered as a blank
// line, so the summarizer saw "no conversation segment provided".
func TestConversationTranscriptRendersToolCallsAndResults(t *testing.T) {
	messages := []inference.Message{
		inference.AssistantMessage("", inference.ToolCall{ID: "a", Name: "apply_patch", Arguments: "{\"path\":\n\"notes.md\"}"}),
		inference.ToolMessage("a", "wrote notes.md"),
	}
	transcript := agentprompt.SummaryTranscript(messages)
	for _, want := range []string{"apply_patch", `{"path": "notes.md"}`, "wrote notes.md"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript %q missing %q", transcript, want)
		}
	}
}

func TestConversationTranscriptTruncatesToolArguments(t *testing.T) {
	messages := []inference.Message{
		inference.AssistantMessage("", inference.ToolCall{ID: "a", Name: "apply_patch", Arguments: strings.Repeat("x", 4000)}),
	}
	if got := len(agentprompt.SummaryTranscript(messages)); got > 200+100 {
		t.Fatalf("transcript length = %d, want the arguments truncated", got)
	}
}

// oversizedSummarizer stands in for a provider that rejects a summarize call
// whose transcript is itself over the window — the shape that produced
// "context could not be reduced" before elision ran first.
type oversizedSummarizer struct {
	limit        int64
	summaryCalls int
}

func (g *oversizedSummarizer) Begin(context.Context, BeginModelRequest) (ModelTurn, error) {
	return ModelTurn{}, errors.New("not used")
}

func (g *oversizedSummarizer) Continue(context.Context, ContinueModelRequest) (ModelTurn, error) {
	return ModelTurn{}, errors.New("not used")
}

func (g *oversizedSummarizer) SummarizeConversation(_ context.Context, messages []inference.Message) (string, error) {
	g.summaryCalls++
	if estimatedMessageTokens(messages) > g.limit {
		return "", errors.New("context length exceeded")
	}
	return "summary of the elided history", nil
}

// One 30k-token tool result into a 20k window: compaction elides first, so the
// summarizer sees a transcript that fits, and the run never reports that the
// context could not be reduced.
func TestPrepareElidesBeforeSummarizingWhenAlreadyOverWindow(t *testing.T) {
	const window = 20000
	huge := strings.Repeat("x", 30000*4)
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("read the file"),
		inference.AssistantMessage("", inference.ToolCall{ID: "call-1", Name: "read_file"}),
		inference.ToolMessage("call-1", huge),
		inference.UserMessage("continue"), inference.AssistantMessage("more"),
	}
	gateway := &oversizedSummarizer{limit: window}
	bus := events.NewOrderedDispatcher(zap.NewNop())
	var notices []string
	defer bus.Register(events.HandlerFunc(func(_ context.Context, event events.Event) error {
		if event.Type == events.EventNotice {
			notices = append(notices, event.Notice.Text)
		}
		return nil
	}))()
	budget := &contextBudget{windowSize: window, gateway: gateway, events: bus, logger: zap.NewNop()}

	compacted, state := budget.Prepare(context.Background(), "s1", messages, contextWindowState{tokenEstimateScale: 1})

	joined := strings.Join(notices, "|")
	if strings.Contains(joined, "could not be reduced") || strings.Contains(joined, "summarization failed") {
		t.Fatalf("notices report a failed compaction: %q", joined)
	}
	if !strings.Contains(joined, "elided old tool output") {
		t.Fatalf("notices do not report the elision: %q", joined)
	}
	if got := contextTokens(compacted, state); got > window*int64(budget.compactThreshold())/100 {
		t.Fatalf("context still over the compaction budget after one pass: %d tokens", got)
	}
}

// The overflowing tool result is usually the newest message — the read the
// model just asked for. Everything older has nothing left to give, so the
// result itself is cut to a head slice rather than leaving the request over
// the window with "context could not be reduced".
func TestElideOldToolOutputCutsTheNewestOversizedResult(t *testing.T) {
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("read pad.md"),
		inference.AssistantMessage("", inference.ToolCall{ID: "call-1", Name: "read_file"}),
		inference.ToolMessage("call-1", strings.Repeat("x", 100000)),
	}
	budget := int64(4000)
	compacted, notice := elideOldToolOutput(messages, planRetention(messages, 0, 1, nil), budget)
	if notice == noReductionNotice {
		t.Fatalf("notice = %q, want a reduction", notice)
	}
	if got := estimatedMessageTokens(compacted); got > budget {
		t.Fatalf("still over budget: %d tokens against %d", got, budget)
	}
	if !strings.Contains(compacted[3].Content, "elided to fit context") || len(compacted[3].Content) < 100 {
		t.Fatalf("newest result was not cut to a head slice: %q", compacted[3].Content[:min(200, len(compacted[3].Content))])
	}
}

// A summary that cannot fit alongside the turn already in flight is not
// prefetched: with a unified KV cache the two requests share one pool and both
// die when it overflows (#499). Compaction still runs synchronously later.
func TestSummaryPrefetchSkippedWhenItWouldOverflowTheWindow(t *testing.T) {
	gateway := &blockingPrefetchGateway{canceled: make(chan error, 1)}
	budget := &contextBudget{gateway: gateway, backgroundContext: context.Background(), logger: zap.NewNop()}
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage(strings.Repeat("a", 400)),
		inference.AssistantMessage(strings.Repeat("b", 400)), inference.UserMessage(strings.Repeat("c", 400)),
		inference.AssistantMessage(strings.Repeat("d", 400)), inference.UserMessage(strings.Repeat("e", 400)),
		inference.AssistantMessage(strings.Repeat("f", 400)),
	}
	budget.prefetchSummary(messages, 1, 0)
	if budget.summaryFuture != nil {
		t.Fatal("prefetch launched with no room left in the server window")
	}
	budget.prefetchSummary(messages, 1, 1<<20)
	if budget.summaryFuture == nil {
		t.Fatal("prefetch skipped with the window wide open")
	}

	// The gate is "segment bigger than headroom", not "or equal": a segment
	// that fits exactly still launches.
	budget.summaryFuture = nil
	segmentTokens := calibratedMessageTokens(summarySegment(messages, budget.plan(messages, 1, false)), 1)
	budget.prefetchSummary(messages, 1, segmentTokens)
	if budget.summaryFuture == nil {
		t.Fatal("prefetch skipped when headroom exactly equals the segment size")
	}
}

// replanMessages is a transcript whose default retention plan leaves a segment
// far bigger than the headroom at the warning threshold — the shape that made
// every prefetch skip in #513.
func replanMessages(count int) []inference.Message {
	messages := []inference.Message{inference.SystemMessage("system"), inference.UserMessage("start")}
	for i := range count {
		messages = append(messages, inference.AssistantMessage(strings.Repeat(string(rune('a'+i)), 400)))
	}
	return messages
}

// A segment that does not fit the headroom is re-planned into a smaller,
// oldest-first chunk that does, instead of skipping the prefetch entirely.
func TestSummaryPrefetchChunksSegmentToHeadroom(t *testing.T) {
	gateway := &compactionFlowGateway{summaries: []string{"chunk summary"}}
	budget := &contextBudget{gateway: gateway, backgroundContext: context.Background(), logger: zap.NewNop()}
	messages := replanMessages(14)
	// Big enough a chunk to clear the coverage gate in resolveSummary: a chunk
	// that covers only a sliver of the middle is re-summarized in full (#529).
	headroom := int64(700)
	if got := calibratedMessageTokens(summarySegment(messages, budget.plan(messages, 1, false)), 1); got <= headroom {
		t.Fatalf("fixture segment already fits: %d tokens", got)
	}

	budget.prefetchSummary(messages, 1, headroom)
	future := budget.summaryFuture
	if future == nil {
		t.Fatal("prefetch skipped instead of re-planning to fit the headroom")
	}
	if future.end <= 2 {
		t.Fatalf("re-planned segment is empty: end = %d", future.end)
	}
	if got := calibratedMessageTokens(messages[2:future.end], 1); got > headroom {
		t.Fatalf("re-planned segment still over headroom: %d > %d", got, headroom)
	}
	<-future.done

	// Compaction later, with more turns appended: the prefetched prefix is
	// reused and the rest of the middle stays verbatim.
	later := append(append([]inference.Message(nil), messages...), inference.UserMessage("next"), inference.AssistantMessage("more"))
	compacted, err := budget.summarizeMessages(context.Background(), later, budget.plan(later, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	// One call: the prefetch's. Compaction adds none.
	if gateway.summaryCalls != 1 {
		t.Fatalf("summarizer called %d times, want only the prefetch", gateway.summaryCalls)
	}
	if !strings.Contains(compacted[2].Content, "chunk summary") {
		t.Fatalf("prefetched summary not used: %#v", compacted[2])
	}
	if len(compacted) != len(later)-future.end+3 {
		t.Fatalf("kept %d messages, want %d verbatim after the summary", len(compacted)-3, len(later)-future.end)
	}
	if compacted[3].Content != later[future.end].Content {
		t.Fatalf("un-summarized remainder was not kept verbatim: %q", compacted[3].Content)
	}
}

// A future whose prefix no longer matches the transcript is stale and ignored.
func TestSummarizeMessagesIgnoresStalePrefetchPrefix(t *testing.T) {
	gateway := &compactionFlowGateway{summaries: []string{"stale", "fresh summary"}}
	budget := &contextBudget{gateway: gateway, backgroundContext: context.Background(), logger: zap.NewNop()}
	messages := replanMessages(10)
	budget.prefetchSummary(messages, 1, 300)
	if budget.summaryFuture == nil {
		t.Fatal("prefetch did not launch")
	}
	<-budget.summaryFuture.done

	edited := append([]inference.Message(nil), messages...)
	edited[2] = inference.AssistantMessage("edited")
	compacted, err := budget.summarizeMessages(context.Background(), edited, budget.plan(edited, 1, false))
	if err != nil {
		t.Fatal(err)
	}
	if gateway.summaryCalls != 2 || !strings.Contains(compacted[2].Content, "fresh summary") {
		t.Fatalf("stale prefetch reused: calls = %d, summary = %#v", gateway.summaryCalls, compacted[2])
	}
}

// stormGateway is a summarizer safe to call from the prefetch goroutine and
// the compaction path at once.
type stormGateway struct{}

func (*stormGateway) Begin(context.Context, BeginModelRequest) (ModelTurn, error) {
	return ModelTurn{}, nil
}
func (*stormGateway) Continue(context.Context, ContinueModelRequest) (ModelTurn, error) {
	return ModelTurn{}, nil
}
func (*stormGateway) SummarizeConversation(context.Context, []inference.Message) (string, error) {
	return "summary", nil
}

// A transcript over the compaction threshold has only a little headroom left,
// so the prefetch chunks the segment down to it (#513) and covers a fraction
// of the plan's middle. Compacting to that chunk frees almost nothing, the
// next turn is over the threshold again, and the shrinking headroom makes the
// next chunk smaller still: 14 compactions in 6 minutes on marathon M1 (#529).
// Compaction must fall back to summarizing the whole middle instead.
func TestCompactionMakesProgressWhenPrefetchCoversOnlyAPrefix(t *testing.T) {
	// window/overhead model a run whose provider prompt is dominated by a
	// large fixed system prompt, which is what leaves the headroom this thin.
	const window, overhead = 14000, 4700
	gateway := &stormGateway{}
	budget := &contextBudget{
		windowSize: window, warningPercent: 70, compactPercent: 80,
		gateway: gateway, backgroundContext: context.Background(),
		events: events.NewOrderedDispatcher(zap.NewNop()), logger: zap.NewNop(),
	}
	messages, state := replanMessages(60), contextWindowState{tokenEstimateScale: 1}
	percent := func() int64 {
		state.providerMessageTokens = estimatedMessageTokens(messages)
		state.providerPromptTokens = state.providerMessageTokens + overhead
		return contextTokens(messages, state) * 100 / window
	}
	if start := percent(); start < 80 {
		t.Fatalf("fixture starts below the compaction threshold: %d%%", start)
	}
	var compactions int
	for range 6 {
		before := percent()
		messages, state = budget.Prepare(context.Background(), "s1", messages, state)
		if percent() < before {
			compactions++
		}
		messages = append(messages, inference.AssistantMessage(strings.Repeat("z", 400)))
	}
	if final := percent(); final >= 80 {
		t.Fatalf("context still at %d%% after %d compactions: compaction is not making progress", final, compactions)
	}
	if compactions > 1 {
		t.Fatalf("compacted %d times; one compaction of the whole middle is enough for this transcript", compactions)
	}
}

// The marathon M1 run-10 shape: the spec read is turn 0, so the second
// compaction summarizes the first summary — and used to drop its notice with
// it, leaving the model quoting spec wording it no longer had (#532).
func TestSummaryFilesSurvivesRepeatedCompaction(t *testing.T) {
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage("implement RELEASE-2.0.md"),
		inference.AssistantMessage("", inference.ToolCall{ID: "a", Name: readFileToolName, Arguments: `{"path":"/ws/RELEASE-2.0.md"}`}),
		inference.ToolMessage("a", strings.Repeat("spec text ", 4096/8)),
		inference.AssistantMessage("one"), inference.UserMessage("two"),
		inference.AssistantMessage("three"), inference.UserMessage("four"),
	}
	budget := &contextBudget{gateway: &compactionFlowGateway{summaries: []string{"first summary", "second summary"}}}
	compact := func(in []inference.Message) []inference.Message {
		out, err := budget.summarizeMessages(context.Background(), in, planRetention(in, 0, 1, nil))
		if err != nil {
			t.Fatal(err)
		}
		return append(out, inference.AssistantMessage("more"), inference.UserMessage("more"))
	}
	twice := compact(compact(messages))
	notice := twice[2].Content
	if !strings.Contains(notice, "/ws/RELEASE-2.0.md") {
		t.Fatalf("second summary = %q, want it to still name the dropped spec read", notice)
	}
	echo := []inference.Message{inference.UserMessage("the summary said" + summaryFilesPrefix + "and then trailed off")}
	if carried := summaryFiles(echo, nil); carried != "" {
		t.Fatalf("notice from a truncated echo = %q, want none", carried)
	}
	if strings.Count(notice, summaryFilesPrefix) != 1 {
		t.Fatalf("second summary = %q, want exactly one carried-forward notice", notice)
	}
}

// afterRewrite used to clear warningSent from the raw estimate while Prepare
// triggers off the calibrated one, so every compaction cycle re-warned and
// launched one more background summary than it needed (#531).
func TestAfterRewriteKeepsWarningInTheTriggerBasis(t *testing.T) {
	messages := []inference.Message{
		inference.SystemMessage("system"), inference.UserMessage(strings.Repeat("x", 10000)),
		inference.AssistantMessage("one"), inference.UserMessage("two"),
	}
	gateway := &compactionFlowGateway{summaries: []string{"a summary"}}
	budget := &contextBudget{gateway: gateway, windowSize: 10000, logger: zap.NewNop()}
	// Raw estimate ≈25% of the window, calibrated ≈75%: past the warning
	// threshold the trigger measures against, short of compaction.
	state := (contextWindowState{warningSent: true, tokenEstimateScale: 3}).afterRewrite(messages, 10000, constants.ContextWarningPercent)
	if !state.warningSent {
		t.Fatal("warningSent cleared for a context the trigger still sees above the warning threshold")
	}
	if _, state = budget.Prepare(context.Background(), "s1", messages, state); budget.summaryFuture != nil {
		t.Fatal("compaction was followed by a redundant summary prefetch")
	}
}
