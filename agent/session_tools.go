package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/platform/events"
	turnpkg "github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/tool_search"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

const maxParallelNativeTools = 4

// errOutputLimitCut is rendered back to the model when its generation ran out
// of output tokens before producing an answer or a tool call.
var errOutputLimitCut = errors.New("your reply was cut off by the output limit before you produced an answer or a tool call; reply again with either one compact tool call or a short final answer, and keep reasoning brief")

// ToolCodeCanceled is the tool-event code for a call the user interrupted;
// the UI renders it as a settled "cancelled" block rather than a failure.
const ToolCodeCanceled = "canceled"

func toolCallKey(call *core.ToolCall) string { return core.CallHash(call.Name, call.Arguments) }

// ResetCallHistory clears request-local denials; a new message can change intent.
func (p *TurnProcessor) ResetCallHistory() {
	p.evidence.Reset()
	p.callMu.Lock()
	defer p.callMu.Unlock()
	p.deniedCalls, p.quotaUsed = nil, nil
}

// quotaExhausted charges one call against the tool's per-run quota and reports
// the refusal once it is used up. The refusal is a flagged completion, not a
// hard failure: the model must see it (so it stops asking) and keep whatever
// the earlier calls returned. Unnamed tools and quotas of 0 or less are
// unlimited.
func (p *TurnProcessor) quotaExhausted(name string) error {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	quota, ok := p.quotas[name]
	if !ok || quota <= 0 {
		return nil
	}
	if p.quotaUsed == nil {
		p.quotaUsed = map[string]int{}
	}
	if p.quotaUsed[name] >= quota {
		refusal := fmt.Sprintf("%s quota %d/%d used this run; continue with what you already have", name, quota, quota)
		return &core.ToolError{Code: "quota", Payload: refusal, Err: errors.New(refusal)}
	}
	p.quotaUsed[name]++
	return nil
}

func (p *TurnProcessor) wasDenied(key string) bool {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	_, ok := p.deniedCalls[key]
	return ok
}

func (p *TurnProcessor) markDenied(key string) {
	p.callMu.Lock()
	defer p.callMu.Unlock()
	if p.deniedCalls == nil {
		p.deniedCalls = map[string]struct{}{}
	}
	p.deniedCalls[key] = struct{}{}
}

func (p *TurnProcessor) Process(ctx context.Context, turn int, modelTurn ModelTurn) (turnResult, []inference.Message, error) {
	answer := strings.TrimSpace(modelTurn.Completion.content)
	p.logger.Debug("model output received", zap.Int("turn", turn), zap.Int("output_len", len(answer)))

	if len(modelTurn.Completion.toolCalls) > 0 {
		return p.nativeToolCall(ctx, turn, modelTurn.Messages, answer, modelTurn.Completion)
	}
	return p.defaultToolCall(ctx, turn, modelTurn.Messages, answer, modelTurn.Completion)
}

func (p *TurnProcessor) defaultToolCall(ctx context.Context, turn int, messages []inference.Message, answer string, completion completionOutput) (turnResult, []inference.Message, error) {
	assistant := inference.AssistantMessage(answer)
	assistant.Reasoning = completion.reasoning
	p.dispatchReasoning(ctx, turn, completion.reasoning)
	messages = append(messages, assistant)
	if completion.syntheticFailure {
		outcome := turnpkg.ToolOutcome{Tool: "unknown", Kind: turnpkg.OutcomeKindParseError, Detail: "tool call arguments unparseable after retry"}
		return turnResult{done: true, answer: answer, usage: completion.usage, outcomes: []turnpkg.ToolOutcome{outcome}, stopReason: stopReasonProviderError}, messages, nil
	}

	// A tool call the server rejected as unparseable is fed back as a tool
	// error so the model can re-issue it, keeping the thread (and the work
	// already done) alive.
	if completion.parseRecovered {
		return p.parseErrorTurn(messages, completion, fmt.Errorf("your last tool call was truncated or unparseable: %s. Keep reasoning short and re-issue the call with compact arguments", completion.parseDetail))
	}

	// A completion cut off by the output limit with no tool call is not an
	// answer, whatever prose survived: feed the cut back as a parse error so
	// the turn loop continues (#443).
	if completion.lengthExhausted {
		return p.parseErrorTurn(messages, completion, errOutputLimitCut)
	}

	output, err := p.syntax.ParseAssistantOutput(answer)
	if err != nil {
		p.logger.Warn("failed parsing assistant output", zap.Int("turn", turn), zap.Error(err))
		return p.parseErrorTurn(messages, completion, err)
	}

	if output.ToolCall != nil {
		toolOutput, rawHash, outcome := p.executeTool(ctx, turn, "", "tool call", output.ToolCall)
		messages = append(messages, p.syntax.RenderToolResult(toolOutput))
		sig := toolCallSignature{Name: output.ToolCall.Name, Key: toolCallKey(output.ToolCall), Args: string(output.ToolCall.Arguments), Result: rawHash}
		return turnResult{done: false, usage: completion.usage, toolCallCount: 1, outcomes: outcomeSlice(outcome), calls: []toolCallSignature{sig}}, messages, nil
	}

	if containsOrphanedToolCallMarkup(output.Reply) {
		p.logger.Warn("assistant reply contains orphaned tool-call markup", zap.Int("turn", turn))
		markupErr := fmt.Errorf("reply contained literal tool-call markup that was not executed; re-issue the call using the tool-call mechanism, not text")
		return p.parseErrorTurn(messages, completion, markupErr)
	}

	p.logger.Debug("assistant reply parsed", zap.Int("turn", turn), zap.Int("reply_len", len(output.Reply)))
	return turnResult{done: true, answer: output.Reply, usage: completion.usage}, messages, nil
}

// parseErrorTurn renders err back to the model and records the parse failure in
// the outcome ledger under the unattributable "unknown" tool, leaving the turn
// unfinished so the model can retry.
func (p *TurnProcessor) parseErrorTurn(messages []inference.Message, completion completionOutput, err error) (turnResult, []inference.Message, error) {
	messages = append(messages, p.syntax.RenderToolResult(syntax.ErrorPayload(err)))
	return turnResult{done: false, usage: completion.usage, outcomes: []turnpkg.ToolOutcome{{Tool: "unknown", Kind: turnpkg.OutcomeKindParseError, Detail: err.Error()}}}, messages, nil
}

// containsOrphanedToolCallMarkup detects a reply that embeds literal tool-call
// syntax as prose (e.g. "<tool_call><function=read_file>…") — the model meant
// to call a tool but emitted text, so finishing the turn would render the blob
// to the user and silently drop the intended call.
func containsOrphanedToolCallMarkup(reply string) bool {
	inFence := false
	for line := range strings.SplitSeq(reply, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && (strings.Contains(line, "<tool_call>") || strings.Contains(line, "<function=")) {
			return true
		}
	}
	return false
}

type nativeCallResult struct {
	message inference.Message
	outcome *turnpkg.ToolOutcome
	sig     toolCallSignature
}

func (p *TurnProcessor) nativeToolCall(ctx context.Context, turn int, messages []inference.Message, answer string, completion completionOutput) (turnResult, []inference.Message, error) {
	assistant := p.codec.AssistantMessage(answer, historySafeToolCalls(completion.toolCalls))
	assistant.Reasoning = completion.reasoning
	p.dispatchReasoning(ctx, turn, completion.reasoning)
	messages = append(messages, assistant)

	run := p.runCallsParallel
	if p.batchNeedsOrder(completion.toolCalls) {
		run = p.runCallsInOrder
	}
	results := run(ctx, turn, completion.toolCalls)

	outcomes, sigs := make([]turnpkg.ToolOutcome, 0, len(results)), make([]toolCallSignature, 0, len(results))
	for _, res := range results {
		messages = append(messages, res.message)
		if res.outcome != nil {
			outcomes = append(outcomes, *res.outcome)
		}
		if res.sig.Key != "" {
			sigs = append(sigs, res.sig)
		}
	}
	return turnResult{done: false, usage: completion.usage, outcomes: outcomes, toolCallCount: len(sigs), calls: sigs}, messages, nil
}

func (p *TurnProcessor) runCallsParallel(ctx context.Context, turn int, calls []inference.ToolCall) []nativeCallResult {
	results, sem := make([]nativeCallResult, len(calls)), make(chan struct{}, maxParallelNativeTools)
	var wg sync.WaitGroup
	for i, nativeCall := range calls {
		wg.Add(1)
		go func(i int, nativeCall inference.ToolCall) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
			}
			results[i] = p.processNativeCallSafe(ctx, turn, nativeCall)
		}(i, nativeCall)
	}
	wg.Wait()
	return results
}

// runCallsInOrder executes a batch that writes or runs code one call at a
// time, in the order the model emitted it, and stops at the first hard
// failure. A small model's `[edit, shell "pytest"]` batch is a sequence,
// not a set: racing the test against the edit, or running it after the edit
// was rejected, reports "the fix didn't work" for a fix that never landed.
func (p *TurnProcessor) runCallsInOrder(ctx context.Context, turn int, calls []inference.ToolCall) []nativeCallResult {
	results := make([]nativeCallResult, 0, len(calls))
	for i, nativeCall := range calls {
		res := p.processNativeCallSafe(ctx, turn, nativeCall)
		results = append(results, res)
		if !isHardFailure(res.outcome) {
			continue
		}
		for _, rest := range calls[i+1:] {
			err := fmt.Errorf("skipped: an earlier tool call in this batch failed (%s: %s); fix that first, then call %s again", res.outcome.Tool, firstLine(res.outcome.Detail), strings.TrimSpace(rest.Name))
			results = append(results, nativeCallResult{message: p.codec.NativeToolResultMessage(rest.ID, syntax.ErrorPayload(err))})
		}
		break
	}
	return results
}

// batchNeedsOrder reports whether the batch contains a plain workspace edit
// (a tool that writes but does not run code, e.g. edit or apply_patch).
// Only then is the batch a sequence — `[edit, shell "pytest"]` — where
// racing the check against the edit, or running it after the edit was
// rejected, reports "the fix didn't work" for a fix that never landed. A
// batch of independent commands (several shell calls, a matrix of toolbox
// probes) keeps running in parallel, so approval queueing and one probe's
// failure do not serialize or skip its neighbours.
func (p *TurnProcessor) batchNeedsOrder(calls []inference.ToolCall) bool {
	if len(calls) < 2 || p.tools == nil {
		return false
	}
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name == "tools" {
			var args tool_search.Args
			if json.Unmarshal([]byte(call.Arguments), &args) != nil || args.Action != "run" {
				return true
			}
			name = args.Name
		}
		names = append(names, name)
	}
	return slices.ContainsFunc(p.tools.Specs(), func(spec core.ToolSpec) bool {
		c := spec.Metadata.Capabilities
		return c.WritesWorkspace && !c.ExecutesCode && slices.Contains(names, spec.Name)
	})
}

func isHardFailure(outcome *turnpkg.ToolOutcome) bool {
	return outcome != nil && slices.Contains([]string{turnpkg.OutcomeKindError, turnpkg.OutcomeKindParseError, turnpkg.OutcomeKindDenied}, outcome.Kind)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// processNativeCallSafe wraps processNativeCall with panic recovery so one
// tool handler crashing in its goroutine degrades to a tool error instead of
// taking down the whole process.
func (p *TurnProcessor) processNativeCallSafe(ctx context.Context, turn int, nativeCall inference.ToolCall) (res nativeCallResult) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("panic in native tool call execution", zap.Int("turn", turn), zap.String("tool_call_id", nativeCall.ID), zap.Any("recover", r), zap.Stack("stack"))
			name := strings.TrimSpace(nativeCall.Name)
			name = cmp.Or(name, "unknown")
			err := fmt.Errorf("panic: %v", r)
			res = nativeCallResult{
				message: p.codec.NativeToolResultMessage(nativeCall.ID, syntax.ErrorPayload(err)),
				outcome: &turnpkg.ToolOutcome{Tool: name, Kind: turnpkg.OutcomeKindError, Detail: err.Error()}}
		}
	}()
	return p.processNativeCall(ctx, turn, nativeCall)
}

// historySafeToolCalls replaces arguments that are not valid JSON with an
// empty object. A truncated argument string kept verbatim in the transcript is
// re-rendered on every later request, and a strict server (llama.cpp) then
// rejects the whole request at chat-template time with an immediate, identical
// HTTP 500 — so the run can never recover, whatever it does next (#539). The
// real arguments are still executed and their failure still reaches the model
// as a tool result; only the copy sent back to the provider is emptied.
func historySafeToolCalls(calls []inference.ToolCall) []inference.ToolCall {
	invalid := func(c inference.ToolCall) bool { return !json.Valid([]byte(c.Arguments)) }
	if !slices.ContainsFunc(calls, invalid) {
		return calls
	}
	out := slices.Clone(calls)
	for i := range out {
		if invalid(out[i]) {
			out[i].Arguments = "{}"
		}
	}
	return out
}

// argumentsCutHint is appended to the tool result for a call whose arguments
// were cut by the output cap, so the model retries with a smaller edit instead
// of re-issuing the same oversized call.
const argumentsCutHint = "; your %s arguments exceeded the output limit — split the tool call into smaller changes and re-issue"

// cutArgumentsBytes is the argument size above which an unparseable call is
// read as an output-cap cut rather than a malformed short call.
const cutArgumentsBytes = 2048

func withCutHint(err error, name, args string) error {
	if len(args) < cutArgumentsBytes {
		return err
	}
	name = cmp.Or(name, "tool")
	return fmt.Errorf("%w"+argumentsCutHint, err, name)
}

func (p *TurnProcessor) processNativeCall(ctx context.Context, turn int, nativeCall inference.ToolCall) nativeCallResult {
	call, err := p.codec.ToolCallFromNative(nativeCall)
	if err != nil {
		p.logger.Warn("failed parsing native tool call", zap.Int("turn", turn), zap.String("tool_call_id", nativeCall.ID), zap.Error(err))
		name := strings.TrimSpace(nativeCall.Name)
		err = withCutHint(err, name, nativeCall.Arguments)
		result := nativeCallResult{
			message: p.codec.NativeToolResultMessage(nativeCall.ID, syntax.ErrorPayload(err)),
			outcome: &turnpkg.ToolOutcome{Tool: "unknown", Kind: turnpkg.OutcomeKindParseError, Detail: err.Error()},
		}
		if name != "" {
			result.outcome.Tool = name
			result.outcome.Kind = turnpkg.OutcomeKindError
			result.sig = toolCallSignature{Name: name, Key: strutil.HashHex(name + "\x00" + nativeCall.Arguments), Args: nativeCall.Arguments}
		}
		return result
	}
	toolOutput, rawHash, outcome := p.executeTool(ctx, turn, nativeCall.ID, "native tool call", call)
	return nativeCallResult{
		message: p.codec.NativeToolResultMessage(nativeCall.ID, toolOutput), outcome: outcome,
		sig: toolCallSignature{Name: call.Name, Key: toolCallKey(call), Args: string(call.Arguments), Result: rawHash},
	}
}

// executeTool runs one call and returns the model-facing (gated) output, a
// hash of the RAW output before gating, and any failure outcome. The repeat
// guard's identity is built from the raw hash: a spill stub embeds a unique
// file name (a fresh sequence number per call), so hashing the gated output
// would never match across repeats of an oversized result.
func (p *TurnProcessor) executeTool(ctx context.Context, turn int, id, label string, call *core.ToolCall) (string, string, *turnpkg.ToolOutcome) {
	resolved, err := p.resolveToolCall(ctx, call)
	if err != nil {
		return p.handleToolError(ctx, turn, id, label, call, 0, err)
	}
	if resolved.Note == "" {
		return p.runTool(ctx, turn, id, label, resolved)
	}
	p.logger.Warn(label+" rerouted from shell", zap.Int("turn", turn), zap.String("tool", resolved.Name), zap.String("from", string(call.Arguments)), zap.String("arguments", string(resolved.Arguments)))
	output, rawHash, outcome := p.runTool(ctx, turn, id, label, resolved)
	return output + "\n\n" + resolved.Note, rawHash, outcome
}

// runTool executes a resolved call; see executeTool.
func (p *TurnProcessor) runTool(ctx context.Context, turn int, id, label string, call *core.ToolCall) (string, string, *turnpkg.ToolOutcome) {
	if id == "" {
		// Default (non-native) path has no native tool-call ID; it is single-call
		// per turn, so (turn, name) is a stable correlation key for the UI.
		id = fmt.Sprintf("%d-%s", turn, call.Name)
	}
	key := toolCallKey(call)
	if p.wasDenied(key) {
		detail := "identical call already denied by the user in this request; not retried"
		p.logger.Info(label+" skipped: previously denied", zap.Int("turn", turn), zap.String("tool", call.Name))
		failPayload := toolPayloadFromCall(call, id, turn, 0, "", detail)
		failPayload.Code = "denied"
		p.dispatchToolEvent(ctx, events.EventToolFailed, failPayload)
		payload := syntax.DeniedRetryPayload(call.Name)
		return payload, strutil.HashHex(payload), &turnpkg.ToolOutcome{Tool: call.Name, Kind: turnpkg.OutcomeKindDenied, Detail: detail}
	}
	toolStarted := time.Now()
	p.logger.Info(label+" started", zap.Int("turn", turn), zap.String("tool", call.Name), zap.String("arguments", string(call.Arguments)))
	p.dispatchToolEvent(ctx, events.EventToolStarted, toolPayloadFromCall(call, id, turn, 0, "", ""))

	if err := p.quotaExhausted(call.Name); err != nil {
		return p.handleToolError(ctx, turn, id, label, call, 0, err)
	}

	// A mis-rendered chat template leaks its delimiters into the last decoded
	// argument. Running the call against the wreckage only produces a
	// "no such path" the model reads as a fact about the repository.
	if err := core.RejectTemplateMarkupArgs(call); err != nil {
		return p.handleToolError(ctx, turn, id, label, call, 0, err)
	}

	ctx = context.WithValue(p.evidence.Context(ctx, id, turn), core.ToolCallIDKey{}, id)
	toolOutput, err := p.tools.HandleCall(core.WithSessionID(ctx, p.sessionID), call)
	// Cancellation wins over whatever the tool reported: a backend that returns a
	// partial result (or its own exit code) after its child was killed must still
	// be recorded as canceled, never completed (issue #300).
	if errors.Is(ctx.Err(), context.Canceled) {
		return p.handleToolCanceled(ctx, turn, id, label, call, time.Since(toolStarted), err)
	}
	if err != nil {
		return p.handleToolError(ctx, turn, id, label, call, time.Since(toolStarted), err)
	}
	duration := time.Since(toolStarted)
	// The event carries the ORIGINAL output (the UI shows what actually ran);
	// only the model-facing string goes through the gate.
	p.dispatchToolEvent(ctx, events.EventToolCompleted, toolPayloadFromCall(call, id, turn, duration, toolOutput, ""))
	rawHash := strutil.HashHex(toolOutput)
	toolOutput, _ = p.gate.cap(call.Name, call.Arguments, toolOutput)
	p.logger.Info(label+" completed", zap.Int("turn", turn), zap.String("tool", call.Name), zap.Duration("duration", duration), zap.Int("output_len", len(toolOutput)))
	return toolOutput, rawHash, nil
}

// handleToolCanceled settles a call whose turn was interrupted. It is the single
// terminal path for cancellation, so the ledger reads canceled and the UI block
// leaves "running" no matter what the tool itself returned.
func (p *TurnProcessor) handleToolCanceled(ctx context.Context, turn int, id, label string, call *core.ToolCall, duration time.Duration, err error) (string, string, *turnpkg.ToolOutcome) {
	detail := "canceled: interrupted by user"
	if err != nil && !errors.Is(err, context.Canceled) {
		detail += " (" + err.Error() + ")"
	}
	p.logger.Info(label+" canceled", zap.Int("turn", turn), zap.String("tool", call.Name), zap.Duration("duration", duration))
	payload := toolPayloadFromCall(call, id, turn, duration, "", detail)
	payload.Code = ToolCodeCanceled
	p.dispatchToolEvent(ctx, events.EventToolFailed, payload)
	result := syntax.ErrorPayload(errors.New(detail))
	return result, strutil.HashHex(result), &turnpkg.ToolOutcome{Tool: call.Name, Kind: turnpkg.OutcomeKindCanceled, Detail: detail}
}

func (p *TurnProcessor) handleToolError(ctx context.Context, turn int, id, label string, call *core.ToolCall, duration time.Duration, err error) (string, string, *turnpkg.ToolOutcome) {
	var toolErr *core.ToolError
	isToolErr := errors.As(err, &toolErr)

	// Completed-but-flagged: the tool returned a code plus real output for the
	// model (e.g. shell exit 1). Surface as a completed event carrying a Code so
	// the UI flags it without counting it as a hard failure.
	if isToolErr && toolErr != nil && toolErr.Payload != "" {
		p.logger.Info(label+" completed with code", zap.Int("turn", turn), zap.String("tool", call.Name), zap.Duration("duration", duration), zap.String("code", toolErr.Code))
		// The event carries the original payload; the gate never empties a
		// non-empty result, so the completed-but-flagged semantics of
		// ToolError.Payload are preserved for the model too. The hash is of the
		// RAW payload, before gating, so the repeat guard's identity survives a
		// spill stub's unique file name.
		payload := toolPayloadFromCall(call, id, turn, duration, toolErr.Payload, "")
		payload.Code = toolErr.Code
		p.dispatchToolEvent(ctx, events.EventToolCompleted, payload)
		rawHash := strutil.HashHex(toolErr.Payload)
		output, _ := p.gate.cap(call.Name, call.Arguments, toolErr.Payload)
		return output, rawHash, nil
	}

	p.logger.Warn(label+" failed", zap.Int("turn", turn), zap.String("tool", call.Name), zap.Duration("duration", duration), zap.Error(err))
	kind, detail, code := turnpkg.OutcomeKindError, err.Error(), ""
	if isToolErr && toolErr != nil {
		code = toolErr.Code
	}
	payload := syntax.ErrorPayload(err)
	if errors.Is(err, context.Canceled) {
		kind, detail, code = turnpkg.OutcomeKindCanceled, "canceled: interrupted by user", ToolCodeCanceled
	}
	var denied *approval.DeniedError
	if errors.As(err, &denied) && denied != nil && kind != turnpkg.OutcomeKindCanceled {
		kind, detail = turnpkg.OutcomeKindDenied, denied.Reason
		code = cmp.Or(code, "denied")
		// Deny is final for this request: remember the call so an identical
		// retry is refused without re-prompting the user (PR #131, test 11).
		p.markDenied(toolCallKey(call))
		payload = syntax.DeniedPayload(err)
	}
	failPayload := toolPayloadFromCall(call, id, turn, duration, "", err.Error())
	failPayload.Code = code
	p.dispatchToolEvent(ctx, events.EventToolFailed, failPayload)
	return payload, strutil.HashHex(syntax.ErrorPayload(err)), &turnpkg.ToolOutcome{Tool: call.Name, Kind: kind, Detail: detail}
}

const readFileToolName = "read"

func outcomeSlice(outcome *turnpkg.ToolOutcome) []turnpkg.ToolOutcome {
	if outcome == nil {
		return nil
	}
	return []turnpkg.ToolOutcome{*outcome}
}

func (p *TurnProcessor) dispatchToolEvent(ctx context.Context, eventType events.EventType, tool events.ToolPayload) {
	if p == nil {
		return
	}
	caps, known := p.toolCaps[tool.Name]
	p.evidence.Observe(eventType, tool, !known || caps.WritesWorkspace || caps.ExecutesCode)
	if p.events == nil {
		return
	}
	// Terminal events must survive cancellation: the dispatcher drops events on a
	// canceled context, which would leave the UI's tool block stuck in "running".
	p.events.Dispatch(context.WithoutCancel(ctx), events.Event{
		Type: eventType, Timestamp: time.Now(), SessionID: p.sessionID, ParentSessionID: p.parentSessionID, Tool: &tool,
	})
}

// dispatchReasoning puts one turn's reasoning text on the events bus (exec
// --json, etc.), when non-empty.
func (p *TurnProcessor) dispatchReasoning(ctx context.Context, turn int, reasoning string) {
	if p == nil || p.events == nil || reasoning == "" {
		return
	}
	p.events.Dispatch(context.WithoutCancel(ctx), events.Event{Type: events.EventReasoning, Timestamp: time.Now(),
		SessionID: p.sessionID, ParentSessionID: p.parentSessionID, Reasoning: &events.ReasoningPayload{Text: reasoning, Turn: turn}})
}

func toolPayloadFromCall(call *core.ToolCall, id string, turn int, duration time.Duration, output string, errText string) events.ToolPayload {
	payload := events.ToolPayload{ID: id, Turn: turn, Duration: duration, OutputBytes: len(output), Output: output, Error: errText}
	if call != nil {
		payload.Name, payload.Arguments = call.Name, string(call.Arguments)
	}
	return payload
}

func (p *TurnProcessor) resolveToolCall(ctx context.Context, call *core.ToolCall) (*core.ToolCall, error) {
	if resolver, ok := p.tools.(interface {
		ResolveToolCall(context.Context, *core.ToolCall) (*core.ToolCall, error)
	}); ok {
		return resolver.ResolveToolCall(core.WithSessionID(ctx, p.sessionID), call)
	}
	return call, nil
}
