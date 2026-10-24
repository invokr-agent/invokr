package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/agentprompt"
	memcore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/skills"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/todo"

	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

type deltaProvider struct {
	deltas []string
	resp   *inference.Response
}

func (p *deltaProvider) Completion(context.Context, inference.Request) (*inference.Response, error) {
	return p.resp, nil
}
func (p *deltaProvider) CompletionWithDeltas(_ context.Context, _ inference.Request, emit func(string)) (*inference.Response, error) {
	for _, delta := range p.deltas {
		emit(delta)
	}
	return p.resp, nil
}
func (*deltaProvider) Metadata(context.Context, string) (clients.ProviderMetadata, error) {
	return clients.ProviderMetadata{}, nil
}

type scriptedProvider struct {
	// rawFinish keeps the scripted responses exactly as written, blank finish
	// reason included, for tests about truncated streams.
	rawFinish bool
	outputs   []*inference.Response
	errors    []error
	requests  []inference.Request
	calls     int
}

func (p *scriptedProvider) Completion(_ context.Context, req inference.Request) (*inference.Response, error) {
	p.requests = append(p.requests, req)
	index := p.calls
	p.calls++
	if index < len(p.errors) && p.errors[index] != nil {
		return nil, p.errors[index]
	}
	if p.outputs[index] == nil {
		return nil, nil
	}
	resp := *p.outputs[index]
	// A well-formed provider always closes with a finish reason; a fixture that
	// leaves it blank would otherwise read as a truncated stream (#499). Copy
	// first: the fixture is shared across calls and, with retries, across
	// attempts within one test.
	if resp.FinishReason == "" && !p.rawFinish {
		resp.FinishReason = inference.FinishStop
	}
	return &resp, nil
}

func (p *scriptedProvider) CompletionOnce(ctx context.Context, params inference.Request) (*inference.Response, error) {
	return p.Completion(ctx, params)
}

func (*scriptedProvider) Metadata(context.Context, string) (clients.ProviderMetadata, error) {
	return clients.ProviderMetadata{}, nil
}

func TestIsToolCallParseError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"classified kind",
			&inference.Error{Kind: inference.KindToolCallParse, Message: "Failed to parse tool call arguments as JSON"},
			true,
		},
		{"unrelated server error", errors.New("server_error: context window exceeded"), false},
		{"connection refused", errors.New("dial tcp [::1]:8080: connect: connection refused"), false},
	}
	for _, tc := range cases {
		if got := isToolCallParseError(tc.err); got != tc.want {
			t.Errorf("%s: isToolCallParseError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCompleteRecoversFromExhaustedToolParseRetries(t *testing.T) {
	parseErr := fmt.Errorf("bad tool call: %w", &inference.Error{Kind: inference.KindToolCallParse})
	provider := &scriptedProvider{errors: []error{parseErr, parseErr}}
	gateway := testModelGateway(provider)

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if output.syntheticFailure || !output.parseRecovered || output.parseDetail == "" || provider.calls != 2 {
		t.Fatalf("output = %#v, calls = %d", output, provider.calls)
	}
	last := provider.requests[1].Messages[len(provider.requests[1].Messages)-1]
	if last.Role != inference.RoleAssistant || last.Content != "\n" {
		t.Fatalf("retry suffix = %#v, want no-thinking assistant prefill", last)
	}
}

// Giving up on parsing must not throw away the work that already ran: the last
// completion asks the model to summarise it, with tools and thinking off.
func TestCompleteGivesUpAfterParseRecoveryBudget(t *testing.T) {
	parseErr := fmt.Errorf("bad tool call: %w", &inference.Error{Kind: inference.KindToolCallParse})
	summary := &inference.Response{Message: inference.Message{Content: "I created site/index.html; the build still needs running."}}
	provider := &scriptedProvider{errors: []error{parseErr, parseErr}, outputs: []*inference.Response{nil, nil, summary}}
	gateway := testModelGateway(provider)
	gateway.parseRecoveries = maxParseRecoveries

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.content, "site/index.html") || output.syntheticFailure || output.parseRecovered {
		t.Fatalf("output = %#v", output)
	}
	last := provider.requests[len(provider.requests)-1]
	if len(last.Tools) != 0 {
		t.Fatalf("the summary completion must disable tools: %#v", last.Tools)
	}
	if suffix := last.Messages[len(last.Messages)-1]; suffix.Role != inference.RoleAssistant || suffix.Content != "\n" {
		t.Fatalf("summary suffix = %#v, want no-thinking assistant prefill", suffix)
	}
}

// When the summary completion fails too, the canned answer still says how much
// already ran rather than claiming nothing was completed — and the count comes
// from the runner's outcome ledger (ADR 0001), not a scan of the transcript.
// A compacted transcript holds fewer RoleTool messages than the ledger has
// recorded, so a naive scan would under-report; the ledger count must win (#538).
func TestCompleteGiveUpFallbackNamesCompletedWork(t *testing.T) {
	parseErr := fmt.Errorf("bad tool call: %w", &inference.Error{Kind: inference.KindToolCallParse})
	provider := &scriptedProvider{errors: []error{parseErr, parseErr, parseErr}}
	gateway := testModelGateway(provider)
	gateway.parseRecoveries = maxParseRecoveries

	// Simulate a post-compaction transcript: only one RoleTool message
	// survived, but the ledger recorded 34 tool calls for the run.
	messages := []inference.Message{inference.UserMessage("run"), inference.ToolMessage("a", "wrote index.html")}
	output, _, err := gateway.complete(context.Background(), messages, "s1", false, false, 34)
	if err != nil {
		t.Fatal(err)
	}
	if !output.syntheticFailure || !strings.HasSuffix(output.content, toolCallFailedAnswer) {
		t.Fatalf("output = %#v", output)
	}
	if !strings.Contains(output.content, "34 tool calls did complete") {
		t.Fatalf("fallback does not name the ledger's completed-work count: %q", output.content)
	}
	if strings.Contains(output.content, "1 tool calls did complete") {
		t.Fatalf("fallback used the transcript scan instead of the ledger count: %q", output.content)
	}
}

func TestCompleteRetriesEmptyCompletionOnce(t *testing.T) {
	empty := &inference.Response{}
	provider := &scriptedProvider{outputs: []*inference.Response{empty, empty}}
	gateway := testModelGateway(provider)

	output, messages, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if output.content != "" || provider.calls != 2 || len(messages) != 1 {
		t.Fatalf("output = %#v, calls = %d, messages = %d", output, provider.calls, len(messages))
	}
}

func TestCompleteOnceStreamsOnlyNativeProse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		deltas []string
		want   string
	}{
		{name: "prose", deltas: []string{"hel", "lo"}, want: "hello"},
		{name: "tagged JSON", deltas: []string{"{", `"type":"reply"}`}, want: ""},
		{name: "tagged reply", deltas: []string{`{"type":"reply",`, `"message":"hello\n`, `world \"friend\""}`}, want: "hello\nworld \"friend\""},
		{name: "tagged unicode reply", deltas: []string{`{"type":"reply","message":"\uD83D`, `\uDE00"}`}, want: "😀"},
		{name: "tool markup", deltas: []string{"<", "tool_call>"}, want: ""},
		{name: "prose before tool call", deltas: []string{"Checking that.\n", `<|tool_call>{"name":"shell"}`, "<|tool_call|>"}, want: "Checking that.\n"},
		{name: "prose with marker split across deltas", deltas: []string{"one <", "|tool_call>{}"}, want: "one "},
		{name: "prose ending in marker prefix", deltas: []string{"a < b <|"}, want: "a < b <|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &deltaProvider{deltas: tc.deltas, resp: &inference.Response{Message: inference.AssistantMessage(tc.want), FinishReason: inference.FinishStop}}
			bus := events.NewOrderedDispatcher(zap.NewNop())
			got := ""
			bus.Register(events.HandlerFunc(func(_ context.Context, event events.Event) error {
				if event.Delta != nil {
					got += event.Delta.Text
				}
				return nil
			}))
			gateway := &OpenAIModelGateway{provider: provider, model: "test", settings: config.AgentConfig{NativeToolRequestsDisabled: true}, tools: &tools.Caller{}, syntax: syntax.NewTaggedJSONSyntax(), logger: zap.NewNop(), events: bus}
			output, err := gateway.completeOnce(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || output.streamed != (tc.want != "") {
				t.Fatalf("delta = %q, output = %#v", got, output)
			}
		})
	}
}

func TestSummarizeConversationModelSelection(t *testing.T) {
	for _, tt := range []struct{ name, configured, want string }{
		{name: "active model fallback", want: "test"},
		{name: "configured summarizer", configured: "summary-model", want: "summary-model"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := &inference.Response{Message: inference.AssistantMessage("compact summary")}
			provider := &scriptedProvider{outputs: []*inference.Response{response}}
			gateway := testModelGateway(provider)
			gateway.settings.SummarizerModel = tt.configured
			summary, err := gateway.SummarizeConversation(context.Background(), []inference.Message{inference.UserMessage("old detail")})
			if err != nil {
				t.Fatal(err)
			}
			if summary != "compact summary" || provider.calls != 1 || provider.requests[0].Model != tt.want {
				t.Fatalf("summary = %q, calls = %d, model = %q", summary, provider.calls, provider.requests[0].Model)
			}
		})
	}
}

func TestSummarizeConversationMetersSpend(t *testing.T) {
	for _, tt := range []struct {
		name             string
		usage            inference.Usage
		wantExactPrompt  int64
		wantExactCompl   int64
		wantEstimateOnly bool
	}{
		{name: "provider-reported usage", usage: inference.Usage{PromptTokens: 123, CompletionTokens: 45}, wantExactPrompt: 123, wantExactCompl: 45},
		{name: "no usage falls back to estimate", wantEstimateOnly: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := &inference.Response{Message: inference.AssistantMessage("compact summary"), Usage: tt.usage}
			gateway := testModelGateway(&scriptedProvider{outputs: []*inference.Response{response}})
			var prompt, completion int64
			gateway.recordSummarizerSpend = func(p, c int64) { prompt, completion = p, c }
			if _, err := gateway.SummarizeConversation(context.Background(), []inference.Message{inference.UserMessage("old detail")}); err != nil {
				t.Fatal(err)
			}
			if tt.wantEstimateOnly {
				if prompt <= 0 || completion <= 0 {
					t.Fatalf("estimate fallback not charged: prompt=%d completion=%d", prompt, completion)
				}
				return
			}
			if prompt != tt.wantExactPrompt || completion != tt.wantExactCompl {
				t.Fatalf("recorded prompt=%d completion=%d, want %d/%d", prompt, completion, tt.wantExactPrompt, tt.wantExactCompl)
			}
		})
	}
}

func testModelGateway(provider clients.LLMProvider) *OpenAIModelGateway {
	return &OpenAIModelGateway{
		provider: provider,
		model:    "test",
		settings: config.AgentConfig{NativeToolRequestsDisabled: true},
		tools:    &tools.Caller{},
		syntax:   syntax.NewTaggedJSONSyntax(),
		logger:   zap.NewNop(),
	}
}

// TestGatewayForwardsPromptCacheExtra covers the opt-in explicit cache markers
// (issue #359): configured fields ride every completion body, and the default
// (nil) leaves the request byte-for-byte as it was before the feature.
func TestGatewayForwardsPromptCacheExtra(t *testing.T) {
	reply := &inference.Response{Message: inference.Message{Content: `{"type":"reply","message":"ok"}`}}
	for name, extra := range map[string]map[string]any{
		"off": nil,
		"on":  {"cache_prompt": true},
	} {
		provider := &scriptedProvider{outputs: []*inference.Response{reply}}
		gateway := &OpenAIModelGateway{
			provider: provider, model: "test", settings: config.AgentConfig{NativeToolRequestsDisabled: true},
			tools: &tools.Caller{}, syntax: syntax.NewTaggedJSONSyntax(), logger: zap.NewNop(),
			events: events.NewOrderedDispatcher(zap.NewNop()), promptCacheExtra: extra,
		}
		if _, err := gateway.Continue(context.Background(), ContinueModelRequest{SessionID: "s1", Messages: []inference.Message{inference.UserMessage("hi")}}); err != nil {
			t.Fatalf("%s: continue: %v", name, err)
		}
		if got := provider.requests[0].Extra; fmt.Sprint(got) != fmt.Sprint(extra) {
			t.Fatalf("%s: request Extra = %v, want %v", name, got, extra)
		}
	}
}

// A stream that ends with content but neither a finish reason nor usage is the
// router dropping a server error frame, not an answer: the turn is re-issued
// and the second attempt stands (#499).
func TestCompleteRetriesTruncatedStream(t *testing.T) {
	noTruncatedStreamBackoff(t)
	truncated := &inference.Response{Message: inference.Message{Content: "I'll check the ind"}}
	good := &inference.Response{
		Message:      inference.Message{Content: "done"},
		FinishReason: inference.FinishStop,
		Usage:        inference.Usage{PromptTokens: 10, CompletionTokens: 2},
	}
	provider := &scriptedProvider{rawFinish: true, outputs: []*inference.Response{truncated, good}}
	gateway := testModelGateway(provider)

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if output.content != "done" || provider.calls != 2 {
		t.Fatalf("output = %#v, calls = %d", output, provider.calls)
	}
}

// Once the retry budget is spent, the truncated-stream error is handed back
// rather than looping forever (#499).
func TestCompleteGivesUpAfterTruncatedStreamRetriesExhausted(t *testing.T) {
	noTruncatedStreamBackoff(t)
	truncated := &inference.Response{Message: inference.Message{Content: "I'll check the ind"}}
	provider := &scriptedProvider{rawFinish: true, outputs: []*inference.Response{truncated, truncated, truncated}}
	gateway := testModelGateway(provider)

	_, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if !errors.Is(err, errTruncatedStream) {
		t.Fatalf("err = %v, want errTruncatedStream", err)
	}
	if provider.calls != 3 {
		t.Fatalf("calls = %d, want 3", provider.calls)
	}
}

func TestTruncatedStreamDetection(t *testing.T) {
	cases := []struct {
		name   string
		output completionOutput
		want   bool
	}{
		{"no finish reason, no usage", completionOutput{content: "cut"}, true},
		{"finish reason present", completionOutput{content: "cut", finishReason: inference.FinishStop}, false},
		{"usage present", completionOutput{content: "cut", usage: turn.TokenUsage{Available: true, LatestPromptTokens: 10}}, false},
		{"no content", completionOutput{}, false},
	}
	for _, tc := range cases {
		if got := truncatedStream(tc.output); got != tc.want {
			t.Errorf("%s: truncatedStream = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSummarizeConversationCapsCompletion covers #506: an uncapped summarizer
// request spent up to 8.8k tokens on a single summary.
func TestSummarizeConversationCapsCompletion(t *testing.T) {
	for _, tt := range []struct {
		name   string
		window int64
		want   int
	}{
		{name: "window unknown", want: constants.MaxSummarizerTokens},
		{name: "large window clamps to the constant", window: 131072, want: constants.MaxSummarizerTokens},
		{name: "window fraction wins", window: 32768, want: 2048},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &scriptedProvider{outputs: []*inference.Response{{Message: inference.AssistantMessage("compact summary")}}}
			gateway := testModelGateway(provider)
			if tt.window > 0 {
				gateway.providerContext = NewProviderContext(tt.window, "test")
			}
			if _, err := gateway.SummarizeConversation(context.Background(), []inference.Message{inference.UserMessage("old detail")}); err != nil {
				t.Fatal(err)
			}
			if got := provider.requests[0].MaxOutputTokens; got != tt.want {
				t.Fatalf("max output tokens = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestSummarizeConversationRetriesEmptyContent covers #515: a reasoning model
// can burn the capped budget inside its reasoning block and return no prose, so
// the summarizer disables thinking and asks once more before failing compaction.
func TestSummarizeConversationRetriesEmptyContent(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		{Message: inference.AssistantMessage("   "), Usage: inference.Usage{CompletionTokens: 40}},
		{Message: inference.AssistantMessage("compact summary"), Usage: inference.Usage{CompletionTokens: 2}},
	}}
	gateway := testModelGateway(provider)
	var completion int64
	gateway.recordSummarizerSpend = func(_, c int64) { completion += c }
	summary, err := gateway.SummarizeConversation(context.Background(), []inference.Message{inference.UserMessage("old detail")})
	if err != nil {
		t.Fatal(err)
	}
	if summary != "compact summary" || provider.calls != 2 || completion != 42 {
		t.Fatalf("summary = %q after %d calls charging %d tokens, want %q after 2 charging 42", summary, provider.calls, completion, "compact summary")
	}
	kwargs, ok := provider.requests[0].Extra["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("summarizer request did not disable thinking: %#v", provider.requests[0].Extra)
	}
}

// Updating task state must not rewrite the cached prefix on Continue or Begin.
func TestTodoUpdateKeepsSystemPrefixStable(t *testing.T) {
	ctx := core.WithSessionID(context.Background(), t.Name())
	caller := &tools.Caller{}
	todo.RegisterTodoTool(caller)
	builder, err := agentprompt.NewBuilder(agentprompt.Config{BasePrompt: "Base."}, agentprompt.Deps{
		Events: events.NewOrderedDispatcher(zap.NewNop()), Memory: noMemory{}, Skills: noSkills{},
		Tools: caller, Syntax: syntax.NewTaggedJSONSyntax(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{outputs: []*inference.Response{
		{Message: inference.AssistantMessage("done")}, {Message: inference.AssistantMessage("done")},
	}}
	gw := testModelGateway(provider)
	gw.prompts = builder
	first, err := gw.Begin(ctx, BeginModelRequest{SessionID: t.Name(), Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := caller.HandleCall(ctx, &core.ToolCall{Name: "todo", Arguments: json.RawMessage(`{"items":[{"task":"CACHE-CANARY","status":"pending"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	messages := append(first.Messages, inference.UserMessage(result))
	if _, err := gw.Continue(ctx, ContinueModelRequest{SessionID: t.Name(), Messages: messages}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(provider.requests[1].Messages, messages) {
		t.Fatal("continued request rewrote the conversation prefix")
	}
	next, err := builder.Build(ctx, agentprompt.Request{SessionID: t.Name(), UserMessage: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if next.System != first.Messages[0].Content || strings.Contains(next.System, "CACHE-CANARY") {
		t.Fatal("todo update changed the system prefix")
	}
}

type noMemory struct{}

func (noMemory) PromptBlocks(context.Context, memcore.PromptRequest) (memcore.PromptBlocks, error) {
	return nil, nil
}

type noSkills struct{}

func (noSkills) Specs(context.Context) ([]skills.Spec, error) { return nil, nil }

// A /model switch drops the served name the last response reported: it names the
// model that answered before the switch, so the model cell falls back to the new
// configured name until the next response reports what answered.
func TestSetModelDropsStaleServedName(t *testing.T) {
	gateway := testModelGateway(&scriptedProvider{})
	gateway.servedModel = "qwen3.8-flash-next-coder-iq1_m"
	gateway.SetModel("cyber-tiel/ornith")
	if got := gateway.ServedModel(); got != "" {
		t.Fatalf("served model after a switch = %q, want empty", got)
	}
	if got := gateway.Model(); got != "cyber-tiel/ornith" {
		t.Fatalf("configured model after a switch = %q, want the new name", got)
	}
}

// TestTruncatedStreamRetryBacksOff: re-issues wait (doubling) instead of firing
// back to back, and a cancel during the wait ends the turn at once.
func TestTruncatedStreamRetryBacksOff(t *testing.T) {
	orig := truncatedStreamBackoff
	truncatedStreamBackoff = 40 * time.Millisecond
	t.Cleanup(func() { truncatedStreamBackoff = orig })
	truncated := &inference.Response{Message: inference.Message{Content: "I'll check the ind"}}

	provider := &scriptedProvider{rawFinish: true, outputs: []*inference.Response{truncated, truncated, truncated}}
	started := time.Now()
	_, _, _ = testModelGateway(provider).complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if waited := time.Since(started); waited < 120*time.Millisecond {
		t.Fatalf("three attempts took %s, want at least 40ms+80ms of backoff", waited)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	provider = &scriptedProvider{rawFinish: true, outputs: []*inference.Response{truncated, truncated, truncated}}
	_, _, _ = testModelGateway(provider).complete(ctx, []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if provider.calls != 1 {
		t.Fatalf("calls = %d after a cancel during backoff, want 1", provider.calls)
	}
}

// noTruncatedStreamBackoff drops the wait between re-issues for one test.
func noTruncatedStreamBackoff(t *testing.T) {
	t.Helper()
	orig := truncatedStreamBackoff
	truncatedStreamBackoff = 0
	t.Cleanup(func() { truncatedStreamBackoff = orig })
}
