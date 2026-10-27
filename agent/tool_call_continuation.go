package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// A tool call cut by the output limit is generated work worth recovering: the
// bench cut a 15.5 KB edit argument mid-string even at n_predict=8192,
// so raising the cap only moves the ceiling. When a completion ends on
// finish_reason "length" with a single tool call whose arguments are not
// valid JSON, the gateway re-issues the request with the partial arguments as
// an assistant prefix and asks for only the remainder, then stitches the two
// halves. This is not generic JSON repair: a stitch that does not parse is
// discarded and the truncated call flows to the existing rejection path,
// because an amputated edit.new written to disk is worse than a failed
// call.

const maxToolCallContinuations = 1

const toolCallContinuePrompt = "Your %s tool call was cut off by the output limit. Your previous message holds its arguments JSON up to the exact cut. Reply with ONLY the remaining characters of that JSON — do not repeat what is already there, no code fences, no commentary — so that previous message + reply form one valid JSON object."

func truncatedToolCall(output completionOutput) bool {
	return output.finishReason == inference.FinishLength &&
		len(output.toolCalls) == 1 &&
		strings.TrimSpace(output.toolCalls[0].Arguments) != "" &&
		!json.Valid([]byte(output.toolCalls[0].Arguments))
}

// maybeContinueToolCall applies at most one truncated-call continuation per
// completion: a call cut by the output limit is generated work worth
// recovering, and a failed stitch leaves the truncated call to the normal
// rejection path unchanged.
func (g *OpenAIModelGateway) maybeContinueToolCall(ctx context.Context, messages []inference.Message, sessionID string, output completionOutput, continuations *int) completionOutput {
	if *continuations >= maxToolCallContinuations || !truncatedToolCall(output) {
		return output
	}
	*continuations++
	events.Notice(ctx, g.events, sessionID, g.parentSessionID, "continuing truncated tool call")
	if fixed, ok := g.continueToolCall(ctx, messages, output); ok {
		return fixed
	}
	return output
}

// continueToolCall asks the model for the rest of the truncated call's
// arguments. The continuation keeps the session's tool definitions with tool
// choice none and runs without streaming — its output is the tail, not prose for the
// user. One attempt only; failure returns ok=false and the caller keeps the
// truncated output as it was.
func (g *OpenAIModelGateway) continueToolCall(ctx context.Context, messages []inference.Message, output completionOutput) (completionOutput, bool) {
	call := output.toolCalls[0]
	g.logger.Warn("continuing truncated tool call", zap.String("tool", call.Name), zap.Int("partial_bytes", len(call.Arguments)))
	continuation := append(append([]inference.Message{}, messages...),
		inference.AssistantMessage(call.Arguments), inference.UserMessage(fmt.Sprintf(toolCallContinuePrompt, call.Name)))
	req := g.buildRequest(g.Model(), withThinkingPrefill(continuation), g.stableToolDefs())
	req.ToolChoice = inference.ToolChoiceNone
	started := time.Now()
	resp, err := g.provider.Completion(ctx, req)
	g.logCompletionOverflow(req, resp, err, started)
	if err != nil || resp == nil || len(resp.Message.ToolCalls) > 0 {
		g.logger.Warn("tool call continuation failed", zap.Error(err))
		return output, false
	}
	args, ok := stitchArguments(call.Arguments, resp.Message.Content)
	if !ok {
		g.logger.Warn("tool call continuation did not stitch", zap.Int("continuation_bytes", len(resp.Message.Content)), zap.String("partial_tail", tail(call.Arguments, 120)), zap.String("continuation_head", head(resp.Message.Content, 300)))
		return output, false
	}
	g.logger.Info("stitched truncated tool call", zap.String("tool", call.Name), zap.Int("stitched_bytes", len(args)))
	call.Arguments = args
	output.toolCalls = []inference.ToolCall{call}
	output.finishReason = inference.FinishToolCalls
	output.usage = addTurnUsage(output.usage, completionFromResponse(resp).usage)
	return output, true
}

// withThinkingDisabled asks a reasoning model to answer the continuation
// directly: on the bench model the continuation otherwise spent its whole
// budget in reasoning_content and returned no text at all. The key is the
// llama.cpp/Qwen chat-template switch; other servers ignore it or reject the
// request, and a rejected continuation falls back to the truncated call.
func withThinkingDisabled(extra map[string]any) map[string]any {
	out := withExtra(extra, "chat_template_kwargs", map[string]any{"enable_thinking": false})
	if _, ok := out["reasoning_effort"]; ok {
		out["reasoning_effort"] = "none"
	}
	return out
}

// withThinkingPrefill ends the request with an assistant prefill so a
// Qwen-style template renders the think block already closed: thinking is off
// for this one completion while the system prompt, which the template varies
// with enable_thinking and reasoning_effort, stays byte-identical and the
// server's prefix cache survives (#577). The prefill must be non-empty —
// llama-server drops an empty trailing assistant message.
func withThinkingPrefill(messages []inference.Message) []inference.Message {
	return append(append([]inference.Message(nil), messages...), inference.AssistantMessage("\n"))
}

func withExtra(extra map[string]any, key string, value any) map[string]any {
	out := maps.Clone(extra)
	if out == nil {
		out = map[string]any{}
	}
	out[key] = value
	return out
}

// stitchArguments combines the partial arguments with the model's
// continuation. Raw concatenation is tried first so no byte of a mid-string
// cut is altered; a continuation that stops inside the last string value is
// closed for it, since the model reliably supplies the text and reliably
// forgets the final `"}`. A fenced or restated reply is accepted only when it
// parses on its own terms. Anything else is refused rather than repaired.
func stitchArguments(partial, continuation string) (string, bool) {
	if joined := partial + continuation; json.Valid([]byte(joined)) {
		return joined, true
	}
	trimmed := trimFences(continuation)
	if joined := partial + trimmed; json.Valid([]byte(joined)) {
		return joined, true
	}
	if len(trimmed) >= minAutoCloseBytes {
		for _, joined := range []string{partial + continuation, partial + trimmed} {
			if closed := joined + closers(joined); json.Valid([]byte(closed)) {
				return closed, true
			}
		}
	}
	// The model started over instead of continuing: a full restatement is
	// still the model's own complete call, so it is usable as-is, with or
	// without prose around it — but only when it carries at least as much as
	// the partial did. The bench saw a 1454-byte edit partial "restated"
	// as {"path":...,"old":"","new":""}, which wrote an empty file.
	if restated := outerObject(trimmed); len(restated) >= len(partial) && json.Valid([]byte(restated)) {
		return restated, true
	}
	// The model repeated the partial before continuing it: drop the echo.
	if i := strings.Index(trimmed, partial); i >= 0 {
		if joined := trimmed[i:]; json.Valid([]byte(joined)) {
			return joined, true
		}
	}
	return "", false
}

// minAutoCloseBytes keeps the auto-close path away from short prose replies
// ("I cannot continue."), which would otherwise be closed into a legitimate
// string value. A real remainder of a cut argument is longer than that.
const minAutoCloseBytes = 64

// closers returns the characters that would close every string, object, and
// array still open at the end of s.
func closers(s string) string {
	var stack []byte
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		inString, stack = scanStructural(c, stack)
	}
	out := make([]byte, 0, len(stack)+1)
	if inString {
		out = append(out, '"')
	}
	for i := len(stack) - 1; i >= 0; i-- {
		out = append(out, map[byte]byte{'{': '}', '[': ']'}[stack[i]])
	}
	return string(out)
}

func scanStructural(c byte, stack []byte) (bool, []byte) {
	switch c {
	case '"':
		return true, stack
	case '{', '[':
		return false, append(stack, c)
	case '}', ']':
		if len(stack) > 0 {
			stack = stack[:len(stack)-1]
		}
	}
	return false, stack
}

func outerObject(s string) string {
	start, end := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func head(s string, n int) string { return s[:min(len(s), n)] }

func tail(s string, n int) string { return s[max(0, len(s)-n):] }

// trimFences strips surrounding whitespace and a markdown code fence, the two
// wrappers models put around a continuation despite being told not to.
func trimFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		} else {
			s = strings.TrimPrefix(s, "```json")
			s = strings.TrimPrefix(s, "```")
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

// addTurnUsage folds the continuation request's spend into the turn's totals
// so the request budget charges both generations.
func addTurnUsage(base, extra turn.TokenUsage) turn.TokenUsage {
	if !extra.Available {
		return base
	}
	if !base.Available {
		return extra
	}
	base.Available, base.LatestPromptTokens, base.LatestCompletionTokens = true, extra.LatestPromptTokens, extra.LatestCompletionTokens
	base.LatestCachedPromptTokens, base.LatestReasoningTokens = extra.LatestCachedPromptTokens, extra.LatestReasoningTokens
	base.TurnPromptTokens += extra.TurnPromptTokens
	base.TurnCompletionTokens += extra.TurnCompletionTokens
	base.TurnTotalTokens += extra.TurnTotalTokens
	return base
}
