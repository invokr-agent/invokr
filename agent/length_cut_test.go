package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

func lengthCutResponse(content string) *inference.Response {
	return &inference.Response{
		Message:      inference.Message{Content: content},
		FinishReason: inference.FinishLength,
	}
}

func TestLengthCutOtherProviderKeepsPlanWithoutLlamaOption(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		{Message: inference.Message{Reasoning: "Keep this plan."}, FinishReason: inference.FinishLength, Usage: inference.Usage{PromptTokens: 100, CompletionTokens: 16384}},
		{Message: inference.Message{ToolCalls: []inference.ToolCall{{ID: "c1", Name: "edit", Arguments: `{}`}}}, FinishReason: inference.FinishToolCalls},
	}}
	gateway := testModelGateway(provider)
	gateway.settings.NPredict = 16384
	gateway.providerContext = NewProviderContext(65536, "test")
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil || provider.calls != 2 || output.reasoning != "Keep this plan." {
		t.Fatalf("calls=%d output=%#v err=%v", provider.calls, output, err)
	}
	if _, ok := provider.requests[1].Extra["continue_final_message"]; ok {
		t.Fatalf("sent llama option to another provider: %#v", provider.requests[1].Extra)
	}
}

func setLlamaReasoning(t *testing.T, gateway *OpenAIModelGateway) {
	t.Helper()
	gateway.settings.NPredict = 16384
	gateway.providerContext = NewProviderContext(0, "test")
	if !gateway.providerContext.ApplyMetadata("test", clients.ProviderMetadata{Reasoning: clients.ReasoningLlamaCpp, ContextWindowTokens: 65536}) {
		t.Fatal("could not set provider metadata")
	}
}

func cutReasoningResponse(reason string) *inference.Response {
	return &inference.Response{Message: inference.Message{Reasoning: reason}, FinishReason: inference.FinishLength,
		Usage: inference.Usage{PromptTokens: 100, CompletionTokens: 16384, TotalTokens: 16484}}
}

func TestLengthCutTruncatedContinuationRetries(t *testing.T) {
	noTruncatedStreamBackoff(t)
	provider := &scriptedProvider{rawFinish: true, outputs: []*inference.Response{
		cutReasoningResponse("A"), {Message: inference.Message{Content: "unfinished partial reply"}},
		{Message: inference.Message{ToolCalls: []inference.ToolCall{{ID: "c1", Name: "edit", Arguments: `{}`}}}, FinishReason: inference.FinishToolCalls},
	}}
	gateway := testModelGateway(provider)
	setLlamaReasoning(t, gateway)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())
	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil || provider.calls != 3 || len(output.toolCalls) != 1 {
		t.Fatalf("truncated continuation accepted: calls=%d output=%#v err=%v", provider.calls, output, err)
	}
}

func TestLengthCutEmptyContinuationAccountsUsage(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		cutReasoningResponse("A"), {FinishReason: inference.FinishStop, Usage: inference.Usage{PromptTokens: 16500, CompletionTokens: 100, TotalTokens: 16600}},
		{Message: inference.Message{ToolCalls: []inference.ToolCall{{ID: "c1", Name: "edit", Arguments: `{}`}}}, FinishReason: inference.FinishToolCalls,
			Usage: inference.Usage{CompletionTokens: 10}},
	}}
	gateway := testModelGateway(provider)
	setLlamaReasoning(t, gateway)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())
	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil || output.usage.TurnCompletionTokens != 16494 {
		t.Fatalf("continuation usage lost: output=%#v err=%v", output, err)
	}
}

func TestLengthCutAtContextBoundaryDoesNotOverflowRetry(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		{Message: inference.Message{Reasoning: "partial plan"}, FinishReason: inference.FinishLength,
			Usage: inference.Usage{PromptTokens: 64581, CompletionTokens: 955, TotalTokens: 65536}},
		{Message: inference.Message{ToolCalls: []inference.ToolCall{{ID: "c1", Name: "edit", Arguments: `{}`}}}, FinishReason: inference.FinishToolCalls},
	}}
	gateway := testModelGateway(provider)
	setLlamaReasoning(t, gateway)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())
	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil || provider.calls != 2 || len(output.toolCalls) != 1 {
		t.Fatalf("boundary retry failed: calls=%d output=%#v err=%v", provider.calls, output, err)
	}
	if len(provider.requests[1].Messages) != 3 || provider.requests[1].Messages[1].Role != inference.RoleUser {
		t.Fatalf("boundary retry added partial reasoning: %#v", provider.requests[1].Messages)
	}
}

// TestLengthCutRetriesWithoutThinking covers #443: a completion that spent its
// whole output budget reasoning is retried once with thinking disabled instead
// of being handed to the runner as a finished (empty) answer.
func TestLengthCutRetriesWithoutThinking(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		lengthCutResponse(""),
		{Message: inference.Message{Content: substantiveAnswer}},
	}}
	gateway := testModelGateway(provider)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider called %d times, want one retry", provider.calls)
	}
	if output.lengthExhausted {
		t.Fatalf("output flagged exhausted after a successful retry")
	}
	last := provider.requests[1].Messages[len(provider.requests[1].Messages)-1]
	if last.Role != inference.RoleAssistant || last.Content != "\n" {
		t.Fatalf("retry suffix = %#v, want no-thinking assistant prefill", last)
	}
	// The nudge text is never appended: the model never reaches it.
	for _, msg := range provider.requests[1].Messages {
		if strings.Contains(msg.Content, "Your previous response was empty") {
			t.Fatalf("empty-completion nudge sent on the length-cut path")
		}
	}
}

// TestLengthCutTwiceKeepsTheTurnOpen covers #443: when the retry is cut too,
// the turn is not final — the loop sees a parse-error outcome and continues.
func TestLengthCutTwiceKeepsTheTurnOpen(t *testing.T) {
	// One cut, then one per retry: the last one exhausts the retries.
	cuts := []*inference.Response{lengthCutResponse("")}
	for range maxLengthRetries {
		cuts = append(cuts, lengthCutResponse("My rstrip() approach is wrong — it strips genuine spaces (e.g. \""))
	}
	provider := &scriptedProvider{outputs: cuts}
	gateway := testModelGateway(provider)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !output.lengthExhausted {
		t.Fatalf("last cut not flagged as exhausted: %#v", output)
	}

	processor := &TurnProcessor{
		events: events.NewOrderedDispatcher(zap.NewNop()), sessionID: "s1",
		syntax: syntax.NewTaggedJSONSyntax(), tools: &tools.Caller{}, logger: zap.NewNop(),
	}
	result, _, err := processor.Process(context.Background(), 0, ModelTurn{Completion: output})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.done {
		t.Fatalf("a length-cut turn was reported done with answer %q", result.answer)
	}
	if len(result.outcomes) != 1 || result.outcomes[0].Kind != "parse-error" {
		t.Fatalf("outcomes = %#v, want one parse-error", result.outcomes)
	}
}

// TestLengthCutWithToolCallIsNotRetried keeps the truncated-tool-call
// continuation path (tool_call_continuation.go) as the owner of that case.
func TestLengthCutWithToolCallIsNotRetried(t *testing.T) {
	resp := lengthCutResponse("")
	resp.Message.ToolCalls = []inference.ToolCall{{ID: "1", Name: "demo", Arguments: `{"a":1}`}}
	provider := &scriptedProvider{outputs: []*inference.Response{resp}}
	gateway := testModelGateway(provider)
	gateway.settings = config.AgentConfig{NativeToolRequestsDisabled: true}
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if provider.calls != 1 || output.lengthExhausted {
		t.Fatalf("calls=%d exhausted=%v, want the tool call left alone", provider.calls, output.lengthExhausted)
	}
}

// substantiveAnswer is long enough to clear degenerateReplyMaxChars, so the
// #503 nudge leaves it alone.
const substantiveAnswer = "I inspected the release script and the tag helper, and the failure comes from the version string being read before the changelog is written. " +
	"The fix is to move the changelog step ahead of the tag step, then re-run the release with the same arguments; nothing else in the pipeline depends on that ordering, " +
	"so no other stage needs to change and the existing tests already cover both steps individually."

// TestDegenerateReplyAfterLengthRetryIsNudged covers #503: a 22-char "No
// response requested." from the no-thinking retry is not the run's answer.
func TestDegenerateReplyAfterLengthRetryIsNudged(t *testing.T) {
	call := &inference.Response{Message: inference.Message{ToolCalls: []inference.ToolCall{{ID: "1", Name: "demo", Arguments: `{"a":1}`}}}}
	provider := &scriptedProvider{outputs: []*inference.Response{
		lengthCutResponse(""),
		{Message: inference.Message{Content: "No response requested."}},
		call,
	}}
	gateway := testModelGateway(provider)
	gateway.settings = config.AgentConfig{NativeToolRequestsDisabled: true}
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider called %d times, want the degenerate reply nudged", provider.calls)
	}
	if len(output.toolCalls) != 1 {
		t.Fatalf("output = %#v, want the tool call from the nudged attempt", output)
	}
	last := provider.requests[2]
	if !strings.Contains(last.Messages[len(last.Messages)-1].Content, "The task is not finished") {
		t.Fatalf("nudge not sent: %#v", last.Messages)
	}
	if kwargs, ok := last.Extra["chat_template_kwargs"].(map[string]any); ok && kwargs["enable_thinking"] == false {
		t.Fatalf("nudged attempt ran with thinking disabled: %#v", last.Extra)
	}
	if got := gateway.RetryCounts()["reasoning_overflow"]; got != 1 {
		t.Fatalf("reasoning_overflow retries = %d, want 1", got)
	}
}

// TestSubstantiveReplyAfterLengthRetryIsAccepted keeps a real answer final.
func TestSubstantiveReplyAfterLengthRetryIsAccepted(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		lengthCutResponse(""),
		{Message: inference.Message{Content: substantiveAnswer}},
	}}
	gateway := testModelGateway(provider)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if provider.calls != 2 || output.content != substantiveAnswer {
		t.Fatalf("calls=%d content=%q, want the answer accepted", provider.calls, output.content)
	}
}

// TestDegenerateNudgeSkippedWhenToolsDisabled covers #504: the exhaustion
// salvage path calls complete with tools and thinking both disabled, so the
// degenerate-reply nudge (which asks for a tool call and re-enables thinking)
// must not fire — the short reply from the length retry is accepted as final.
func TestDegenerateNudgeSkippedWhenToolsDisabled(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		lengthCutResponse(""),
		{Message: inference.Message{Content: "No response requested."}},
	}}
	gateway := testModelGateway(provider)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", true, true, 0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider called %d times, want no degenerate-nudge retry", provider.calls)
	}
	if output.content != "No response requested." {
		t.Fatalf("content = %q, want the short reply accepted as final", output.content)
	}
}

// TestShortReplyWithoutALengthCutIsUntouched: the nudge is scoped to the
// reasoning-overflow path, so an ordinary short answer still ends the turn.
func TestShortReplyWithoutALengthCutIsUntouched(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		{Message: inference.Message{Content: "Yes — the flag is already set."}},
	}}
	gateway := testModelGateway(provider)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	_, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider called %d times, want the plain reply left alone", provider.calls)
	}
	if gateway.RetryCounts() != nil {
		t.Fatalf("retry counts = %#v, want none", gateway.RetryCounts())
	}
}

// TestLengthCutRetryDisablesThinkingForOneCallOnly covers #596: the
// no-thinking retry applies to the retried completion alone. The next turn
// thinks again; a run-wide switch left one aider_transpose run with 35 calls
// and no reasoning after a single cut.
func TestLengthCutRetryDisablesThinkingForOneCallOnly(t *testing.T) {
	provider := &scriptedProvider{outputs: []*inference.Response{
		lengthCutResponse(""),
		{Message: inference.Message{Content: substantiveAnswer}},
		{Message: inference.Message{Content: substantiveAnswer}},
	}}
	gateway := testModelGateway(provider)
	gateway.events = events.NewOrderedDispatcher(zap.NewNop())

	if _, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("next")}, "s1", false, false, 1); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider called %d times, want cut + retry + next turn", provider.calls)
	}
	if last := provider.requests[1].Messages[len(provider.requests[1].Messages)-1]; last.Role != inference.RoleAssistant || last.Content != "\n" {
		t.Fatalf("retry suffix = %#v, want no-thinking assistant prefill", last)
	}
	if last := provider.requests[2].Messages[len(provider.requests[2].Messages)-1]; last.Role == inference.RoleAssistant {
		t.Fatalf("next turn still had assistant prefill: %#v", last)
	}
}
