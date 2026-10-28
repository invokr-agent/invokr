package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/tools"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// truncatedEditArguments is an edit argument string cut mid-value by the
// output cap, as the M4 marathon run produced (#539).
func truncatedEditArguments() string {
	return `{"path":"norm.py","old":"","new":"` + strings.Repeat("x", cutArgumentsBytes)
}

// TestTruncatedToolCallLeavesTranscriptParseable covers #539: a tool call whose
// arguments were cut by the output cap must not enter the transcript verbatim,
// because a strict server then rejects every later request at chat-template
// time with an identical, immediate error and the run can never recover.
func TestTruncatedToolCallLeavesTranscriptParseable(t *testing.T) {
	processor := &TurnProcessor{
		events: events.NewOrderedDispatcher(zap.NewNop()), sessionID: "s1",
		syntax: syntax.NewTaggedJSONSyntax(), codec: OpenAINativeToolCodec{},
		tools: &tools.Caller{}, logger: zap.NewNop(),
	}
	call := inference.ToolCall{ID: "c1", Name: "edit", Arguments: truncatedEditArguments()}
	result, messages, err := processor.Process(context.Background(), 0,
		ModelTurn{Completion: completionOutput{toolCalls: []inference.ToolCall{call}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.done {
		t.Fatalf("run ended on a truncated tool call: %#v", result)
	}
	for _, m := range messages {
		for _, c := range m.ToolCalls {
			if !json.Valid([]byte(c.Arguments)) {
				t.Fatalf("transcript kept unparseable arguments: %q", c.Arguments)
			}
		}
	}
	last := messages[len(messages)-1].Content
	if !strings.Contains(last, "exceeded the output limit") || !strings.Contains(last, "smaller changes") {
		t.Fatalf("tool result = %q, want the split-the-tool-call hint", last)
	}
	if !strings.Contains(last, "edit") {
		t.Fatalf("tool result = %q, want the tool named", last)
	}
}

// A short malformed call is an ordinary mistake, not an output-cap cut, so it
// keeps the plain parse error rather than being told to split an edit.
func TestShortMalformedArgumentsKeepPlainError(t *testing.T) {
	err := withCutHint(errOutputLimitCut, "edit", "{oops")
	if strings.Contains(err.Error(), "output limit — split") {
		t.Fatalf("err = %q, want no cut hint", err)
	}
}

// TestIdenticalParseRejectionSpendsOneRecovery covers #539: the M4 run spent
// its whole recovery budget re-issuing a request the server rejected in 5 ms
// with a byte-identical error every time. A rejection that repeats verbatim is
// the request being refused, not a fresh mistake, so it must not buy another
// recovery.
func TestIdenticalParseRejectionSpendsOneRecovery(t *testing.T) {
	parseErr := fmt.Errorf("bad tool call: %w", &inference.Error{Kind: inference.KindToolCallParse, Message: "column 14885"})
	provider := &scriptedProvider{errors: []error{parseErr, parseErr, parseErr, parseErr, parseErr, parseErr}}
	gateway := testModelGateway(provider)
	messages := []inference.Message{inference.UserMessage("run")}

	first, _, err := gateway.complete(context.Background(), messages, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !first.parseRecovered {
		t.Fatalf("first rejection = %#v, want a recovery", first)
	}
	second, _, err := gateway.complete(context.Background(), messages, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.parseRecovered {
		t.Fatalf("identical rejection bought another recovery: %#v", second)
	}
	if !second.syntheticFailure {
		t.Fatalf("second rejection = %#v, want a surfaced failure", second)
	}
}

// TestProviderGiveUpIsNotReportedAsAnswered covers #539: the give-up answer
// after repeated provider rejections reads exactly like a real one, so the
// ledger — the source of truth (ADR 0001) — must name it as a provider error
// rather than leaving the run indistinguishable from one that answered.
func TestProviderGiveUpIsNotReportedAsAnswered(t *testing.T) {
	parseErr := fmt.Errorf("bad tool call: %w", &inference.Error{Kind: inference.KindToolCallParse})
	provider := &scriptedProvider{errors: []error{parseErr, parseErr, parseErr}}
	gateway := testModelGateway(provider)
	gateway.parseRecoveries = maxParseRecoveries
	output, _, err := gateway.complete(context.Background(), []inference.Message{inference.UserMessage("run")}, "s1", false, false, 0)
	if err != nil {
		t.Fatal(err)
	}

	processor := &TurnProcessor{events: events.NewOrderedDispatcher(zap.NewNop()), syntax: syntax.NewTaggedJSONSyntax(), logger: zap.NewNop()}
	result, _, err := processor.Process(context.Background(), 0, ModelTurn{Completion: output})
	if err != nil {
		t.Fatal(err)
	}
	if !result.done {
		t.Fatalf("result = %#v, want the run finished", result)
	}
	if result.stopReason != stopReasonProviderError {
		t.Fatalf("stopReason = %q, want %q", result.stopReason, stopReasonProviderError)
	}
}
