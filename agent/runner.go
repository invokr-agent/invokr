package agent

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	turnpkg "github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools/core"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// maxIdenticalToolCallRepeats is how many turns in a row may issue the identical
// tool call(s) before the runner nudges the model to change approach, mirroring
// the tool-call-parse give-up shape in model_gateway.go.
const maxIdenticalToolCallRepeats = 3

// maxRepeatNudges caps how many times one request may nudge a repeating model
// before giving up gracefully — a model the nudge cannot unstick should not
// burn the remaining turn/token budget re-looping (issue #295).
const maxRepeatNudges = 2

// maxSameToolFailures is the looser threshold for one tool failing over and
// over with a *different* error each time — a model guessing at a format. The
// exact-error key never repeats there, so the detailed guard never fires and
// the run burns its whole budget (observed: 18 of 25 turns on malformed
// patches). Counted per tool instead, and higher than the identical-call
// threshold because different errors can still mean genuine progress.
const maxSameToolFailures = 5

// maxConsecutiveFailedTurns catches the same wander when the model rotates
// between tools — apply_patch, then shell, then read_file — so no single tool
// reaches maxSameToolFailures. Turns that land a clean tool call reset it, so
// tripping this means many turns in a row produced nothing but errors.
const maxConsecutiveFailedTurns = 8

// maxNearDuplicateCalls is how many consecutive single tool calls to the same
// tool with near-identical arguments trip the guard. The exact-signature
// guard never fires on a model re-running one script with a constant changed
// each turn (round 5: ~30 code_interpreter calls in a row, 37-40 turns, then
// the wall clock); the threshold is looser than the identical one because an
// iterate-and-test loop that is actually converging looks the same for a few
// turns.
const maxNearDuplicateCalls = 5

// nearDuplicateSimilarity is the character-bigram Jaccard score at or above
// which two argument strings count as the same call. Measured against
// bigramJaccard: a 500-character script with one constant changed scores
// ~0.98 and a 60-character one ~0.96, while sibling paths the model reads in
// sequence (tests/fixtures/case_01.json vs case_02.json = 0.89,
// user_repository_impl.go vs order_repository_impl.go = 0.86) stay under it.
// 0.85 tripped the guard on five numbered fixtures in a row.
const nearDuplicateSimilarity = 0.95

const timeWarningPercent = 75

// salvagePercent is the share of RunTimeout the wrap-up completion gets once
// the budget is spent, so a run ends by about 125% of RunTimeout whatever the
// model does. Without it the salvage call was unbudgeted and a harness
// deadline set just above RunTimeout killed the run mid-salvage.
const salvagePercent = 25

// internalNudgePrefix marks a message invokr injects mid-run. It frames the
// message as a step in the user's task: worded as "internal retry", a model
// read it as setup with no task and said so in its reasoning (A10).
const internalNudgePrefix = "[Continuing your task above] "

// exhaustionWrapUp is the instruction the salvage completion carries. Without
// it the model, handed a tool-free turn with no explanation, narrates its next
// tool call; the markup is then stripped and the stored answer is a fragment
// of intent ("Now let me re-run and inspect the full output:") (#444).
const exhaustionWrapUp = internalNudgePrefix + "The budget is exhausted and tools are now disabled. Do not plan further steps and do not write tool calls. Reply in plain text with the final answer, which files you wrote, and what remains undone."

// repeatExhaustionWrapUp replaces exhaustionWrapUp when the run ends because
// the model kept repeating identical tool calls after the nudge cap, rather
// than a turn/time/token budget running out. The work is often already done
// at that point (tests green, diff complete) — the model was just looping —
// so the ask is a plain summary rather than the budget-exhaustion framing,
// which reads as an abort even when nothing is actually wrong.
const repeatExhaustionWrapUp = internalNudgePrefix + "Stop calling tools. Summarize what you changed and its current state."

// repeatExhaustionReasonPrefix opens nudgeRepeat's give-up reason; gracefulExhaustion
// matches on it to pick repeatExhaustionWrapUp over exhaustionWrapUp.
const repeatExhaustionReasonPrefix = "model kept repeating identical tool calls"

// preExhaustionWarning is sent once, one wrap-up turn before the budget runs
// out, so the model can land its remaining work itself. One-shot on purpose:
// repeated meta-nudges add churn.
const preExhaustionWarning = internalNudgePrefix + "Budget almost spent: about %d turns left. Finish now — write any remaining file, then reply with the final answer."

// preExhaustionTurns is how many turns before MaxTurns the warning fires; the
// token budget has its own threshold (spend.go requestWarningPercent).
const preExhaustionTurns = 3

// preExhaustionTimeWarning is the wall-clock variant: the model has no sense of
// elapsed time, so "turns left" would be misleading.
const preExhaustionTimeWarning = internalNudgePrefix + "Budget almost spent: time is nearly up. Finish now — write any remaining file, then reply with the final answer."

// nearDuplicateNudge names the tool, since the model is not literally
// repeating itself and the generic wording ("the same call repeated") reads as
// wrong to it.
const nearDuplicateNudge = internalNudgePrefix + "Your last %d %s calls were near-identical and returned unchanged output. Repeating them has shown no new information. Step back: state what the output tells you, change the approach, or answer with what you already know."

// summaryNudge is sent once when a thread that ran tools ends on an answer
// with nothing of the task in it — a bare greeting or "Understood." Small
// models produce these after a successful tool loop, and the run then reads as
// having done nothing.
const summaryNudge = internalNudgePrefix + "Your last reply had no answer for the task. Continue the task: if work remains, keep going with tool calls; if it is finished, give the final summary of what you did — which files you changed and why, in a few sentences."

// maxTaskFreeAnswer is how short a final answer must be to be read as
// task-free. A long answer naming none of the work is still an answer; the
// observed failures were all one or two sentences.
const maxTaskFreeAnswer = 240

// maxWorkTerms caps the terms collected from tool arguments: the check only
// needs enough of them to recognise a summary, not every path a run touched.
const maxWorkTerms = 64

// repeatedToolCallNudge steers the model to a different call, never to giving
// up: small models generalise a bare "stop repeating it" into "stop using this
// tool at all" (a throttled web_search read as "the web is down"), so the nudge
// names the concrete variations to try instead.
const repeatedToolCallNudge = internalNudgePrefix + "That exact call was already made and returned the same result — repeating it will not change anything, but the task is still worth finishing. Vary it instead: change the arguments (different keywords, another path or pattern), use a different tool for the same goal, or re-read that tool's example for the exact format it expects. Only answer directly from what you already know once you have tried a different call."

type runState struct {
	usage         turnpkg.TokenUsage
	failures      []turnpkg.ToolOutcome
	callCount     int
	messages      []inference.Message
	contextState  contextWindowState
	guard         repeatGuard
	nudges        int
	warned        bool
	nudgeAppended bool
	summaryNudged bool
	noToolNudges  int
	parseStreak   int
	workTerms     []string
}

// repeatGuard tracks how often each tool-call signature is issued within a
// request so the runner can nudge the model to change approach after
// maxIdenticalToolCallRepeats. Counts are per signature, not per consecutive
// run, so identical calls separated by other turns are still detected.
type repeatGuard struct {
	counts                    map[string]int
	recent                    []string
	lastFailure               string
	failures                  int
	lastFailureTool           string
	toolFailures, failedTurns int
	lastCall                  toolCallSignature
	similarCalls              int
}

// observeNearDuplicate nudges after near-identical single-tool calls return
// unchanged raw output. Other calls or changed output reset the streak. It runs
// before observe so exact-signature bookkeeping is untouched.
func (g *repeatGuard) observeNearDuplicate(calls []toolCallSignature) bool {
	if len(calls) != 1 {
		g.lastCall, g.similarCalls = toolCallSignature{}, 0
		return false
	}
	call := calls[0]
	if call.Name == g.lastCall.Name && call.Result == g.lastCall.Result && bigramJaccard(call.Args, g.lastCall.Args) >= nearDuplicateSimilarity {
		g.similarCalls++
	} else {
		g.similarCalls = 1
	}
	g.lastCall = call
	if g.similarCalls < maxNearDuplicateCalls {
		return false
	}
	g.reset()
	return true
}

// bigramJaccard scores two strings by the Jaccard index of their character
// bigrams. Identical strings score 1; one constant changed in a 40-character
// command scores ~0.9 and in a 500-character script ~0.99; a different command
// lands well under the threshold. Character rather than token bigrams because
// the grind calls are often short, and one changed token out of ten already
// drops a token-bigram score to 0.6. O(len) per turn.
func bigramJaccard(a, b string) float64 {
	setA, setB := bigrams(a), bigrams(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 1
	}
	var inter int
	for k := range setA {
		if setB[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(setA)+len(setB)-inter)
}

func bigrams(s string) map[string]bool {
	runes := []rune(s)
	out := make(map[string]bool, len(runes))
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i:i+2])] = true
	}
	if len(runes) == 1 {
		out[s] = true
	}
	return out
}

// observe records the turn's tool-call signature and reports whether the
// repeat threshold has just been reached. An empty key (no tool calls, e.g.
// parse-error retries) leaves the guard state intact.
func (g *repeatGuard) observe(turnKey, failureKey, toolKey string) bool {
	if g.observeFailureTool(toolKey) {
		return true
	}
	if turnKey == "" {
		return g.observeFailure(failureKey)
	}
	g.recent = append(g.recent, turnKey)
	if len(g.recent) > 4 {
		g.recent = g.recent[len(g.recent)-4:]
	}
	if g.counts == nil {
		g.counts = map[string]int{}
	}
	g.counts[turnKey]++
	identical := g.counts[turnKey] >= maxIdenticalToolCallRepeats
	alternating := len(g.recent) == 4 && g.recent[0] == g.recent[2] && g.recent[1] == g.recent[3] && g.recent[0] != g.recent[1]
	if !identical && !alternating && !g.observeFailure(failureKey) {
		return false
	}
	g.reset()
	return true
}

func (g *repeatGuard) observeFailure(key string) bool {
	if key == "" {
		g.lastFailure, g.failures = "", 0
		return false
	}
	if key == g.lastFailure {
		g.failures++
	} else {
		g.lastFailure, g.failures = key, 1
	}
	if g.failures < maxIdenticalToolCallRepeats {
		return false
	}
	g.reset()
	return true
}

// observeFailureTool counts consecutive turns whose failures come from the
// same tool, whatever the error text, and separately counts consecutive turns
// that failed at all. A turn with no failing tool clears both.
func (g *repeatGuard) observeFailureTool(key string) bool {
	if key == "" {
		g.lastFailureTool, g.toolFailures, g.failedTurns = "", 0, 0
		return false
	}
	if key == g.lastFailureTool {
		g.toolFailures++
	} else {
		g.lastFailureTool, g.toolFailures = key, 1
	}
	g.failedTurns++
	if g.toolFailures < maxSameToolFailures && g.failedTurns < maxConsecutiveFailedTurns {
		return false
	}
	g.reset()
	return true
}

func (g *repeatGuard) reset() { *g = repeatGuard{} }

func (r *Runner) Run(ctx context.Context, req RunRequest) (turnpkg.AgentResponse, error) {
	started := time.Now()
	r.logger.Info("agent request started", zap.Int("message_len", len(req.Message)))
	r.applyProviderContext()

	r.turnProcessor.ResetCallHistory()
	r.mu.RLock()
	state := runState{
		usage: r.usage.NewTurnUsage(), messages: append([]inference.Message(nil), r.messages...), contextState: r.contextState,
	}
	r.mu.RUnlock()
	r.spend.beginRequest()
	// Non-nil so callers (e.g. JSONL output) serialize an empty [] rather than null.
	state.failures = []turnpkg.ToolOutcome{}
	budget := newTurnBudget(r.settings, r.toolCaps)
	exhaustion := ""
	// All work shares the request deadline, including tools and compaction.
	// Salvage gets its own deadline below, while caller cancellation still wins.
	workCtx, cancelWork := r.withRunDeadline(ctx, started, r.settings.RunTimeout)
	defer cancelWork()

	for turn := 0; r.turnAvailable(ctx, req, &budget, &state); turn++ {
		r.applyPendingModel()
		state.usage = state.usage.WithContextWindowTokens(r.providerContext.ContextWindowTokens())
		turnStarted := time.Now()
		state.messages, state.contextState = r.prepareAndStoreContext(workCtx, req.SessionID, state.messages, state.contextState)
		modelTurn, err := r.modelTurn(workCtx, turn, req, &state)
		if err != nil {
			if reason, cut := r.cutByTimeBudget(ctx, started); cut {
				r.logger.Warn("completion cut by the time budget mid-turn", zap.Int("turn", turn), zap.Duration("duration", time.Since(turnStarted)), zap.Error(err))
				exhaustion = reason
				break
			}
			r.logger.Error("agent turn failed", zap.Int("turn", turn), zap.Duration("duration", time.Since(turnStarted)), zap.Error(err))
			return r.failedResponse(state), err
		}
		r.afterModelTurn(&modelTurn, &state)
		result, nextMessages, err := r.turnProcessor.Process(workCtx, turn, modelTurn)
		if err = cmp.Or(ctx.Err(), err); err != nil {
			r.logger.Error("agent turn failed", zap.Int("turn", turn), zap.Duration("duration", time.Since(turnStarted)), zap.Error(err))
			return r.failedResponse(state), err
		}
		result.answer = r.answerOrFallback(result, turn, modelTurn.Completion)
		r.recordTurn(&state, &result, modelTurn, nextMessages)
		if response, finished := r.finishOrRetry(ctx, req, result, modelTurn.Completion.streamed, &state, started, turn); finished {
			return response, nil
		}
		if reason, stop := r.stopAfterTurn(&state, modelTurn.Completion, result, started); stop {
			exhaustion = reason
			break
		}

		nudges := state.nudges
		if reason, stop := r.nudgeRepeat(ctx, req, turn, &state, result); stop {
			exhaustion = reason
			break
		}
		budget.observe(result, r.nudgeParseFailure(ctx, req, &state, result, state.nudges != nudges))
		r.warnNearExhaustion(turn, &state, started, &budget)
	}
	if exhaustion == "" {
		exhaustion = stepLimitReason(budget.limitTurns())
	}

	state.messages = stripTrailingInternalNudge(state.messages)
	return r.gracefulExhaustion(ctx, req, state, started, exhaustion)
}

func (r *Runner) recordTurn(state *runState, result *turnResult, modelTurn ModelTurn, next []inference.Message) {
	state.usage = state.usage.AccumulateTurnUsage(result.usage)
	r.spend.observeTurn(result.usage)
	state.contextState = updateProviderUsage(state.contextState, result.usage, modelTurn.Messages, state.messages)
	state.failures = append(state.failures, result.outcomes...)
	state.callCount += result.toolCallCount
	state.messages = next
	state.workTerms = appendWorkTerms(state.workTerms, result.calls)
}

// turnAvailable reports whether another turn may run. Past the budget the run
// is extended once per exhaustion while it is still making progress, never
// past the hard ceiling. A grant re-arms the one-shot wrap-up warning: the
// model was told a turn count against the old limit, so it must be told again
// against the new one or it wraps up with budget still in hand.
func (r *Runner) turnAvailable(ctx context.Context, req RunRequest, budget *turnBudget, state *runState) bool {
	if !budget.exhausted() {
		return true
	}
	if budget.iterations >= budget.hardCeiling || !budget.extend() {
		return false
	}
	state.warned = false
	r.logger.Info("turn budget extended on progress", zap.Int("extension", budget.extension), zap.Float64("spent", budget.spent), zap.Float64("limit", budget.limit))
	events.Notice(ctx, r.events, req.SessionID, r.parentSessionID,
		fmt.Sprintf("turns.extended: +%d (spent %.1f/%.0f)", budget.extension, budget.spent, budget.limit))
	return true
}

// afterModelTurn does the bookkeeping a completion needs before the turn is
// processed: the local token estimate for providers that never report usage,
// and dropping the trailing nudge, which was for the model's eyes only.
func (r *Runner) afterModelTurn(modelTurn *ModelTurn, state *runState) {
	if !state.usage.Available {
		// The estimate only backs providers that never report usage; once
		// Available latches true it stays true, so skip the walk.
		r.spend.observeEstimate(*modelTurn)
	}
	if state.nudgeAppended {
		modelTurn.Messages, state.nudgeAppended = modelTurn.Messages[:len(modelTurn.Messages)-1], false
	}
}

// modelTurn asks the model for the next turn. On a provider "context length
// exceeded" rejection it forces one compaction, rebuilds the transcript, and
// retries once; a second rejection flows back to the normal failure path.
func (r *Runner) modelTurn(ctx context.Context, turn int, req RunRequest, state *runState) (ModelTurn, error) {
	modelTurn, err := r.nextModelTurn(ctx, turn, req, state.messages, state.callCount)
	if !clients.IsContextLengthExceeded(err) {
		return modelTurn, err
	}
	r.logger.Warn("provider rejected request as over context; forcing compaction and retrying once", zap.Error(err))
	events.Notice(ctx, r.events, req.SessionID, r.parentSessionID, "context length exceeded; compacting and retrying")
	r.budget.RequestCompaction()
	state.messages, state.contextState = r.prepareAndStoreContext(ctx, req.SessionID, state.messages, state.contextState)
	return r.nextModelTurn(ctx, turn, req, state.messages, state.callCount)
}

// nudgeRepeat feeds the turn's tool calls to the repeat guard and, when the
// model is looping, appends a nudge. It reports an exhaustion reason once the
// nudge cap is hit. Only turns that made tool calls are tracked; turns without
// them (e.g. parse-error retries) leave the guard state intact so repeated
// calls interleaved with non-tool turns are still detected.
func (r *Runner) nudgeRepeat(ctx context.Context, req RunRequest, turn int, state *runState, result turnResult) (string, bool) {
	nudge := repeatedToolCallNudge
	if state.guard.observeNearDuplicate(result.calls) {
		nudge = fmt.Sprintf(nearDuplicateNudge, maxNearDuplicateCalls, result.calls[0].Name)
	} else if !state.guard.observe(turnSignatureKey(result.calls), failureSignatureKey(result.outcomes), failureToolKey(result.outcomes)) {
		return "", false
	}
	if state.nudges++; state.nudges > maxRepeatNudges {
		return fmt.Sprintf("%s after %d nudges", repeatExhaustionReasonPrefix, maxRepeatNudges), true
	}
	r.logger.Warn("repeated tool call; nudging model to change approach", zap.Int("turn", turn), zap.Bool("near_duplicate", nudge != repeatedToolCallNudge))
	r.nudge(ctx, req, state, nudge, "repeated tool call detected; asking model to change approach")
	return "", false
}

// parseFailureNudge steers a model whose tool calls keep coming back
// unparseable: two in a row means the arguments are the problem, not the
// tool, so it is told to make the edit smaller (#490).
const parseFailureNudge = internalNudgePrefix + "Your last two tool calls could not be parsed; split the edit into smaller calls or use apply_patch."

// nudgeParseFailure counts consecutive turns that produced no parseable tool
// call and, on the second, appends one steer. It reports whether the budget
// should treat the turn as nudged: the repeat guard fired, or the run is in
// such a streak — a window whose turns never parsed is not progress and must
// not buy an extension (#490).
func (r *Runner) nudgeParseFailure(ctx context.Context, req RunRequest, state *runState, result turnResult, nudged bool) bool {
	if !hasParseFailure(result.outcomes) {
		state.parseStreak = 0
		return nudged
	}
	// No other nudge can be pending here: a parse-failure turn has no calls,
	// no error outcomes for the repeat guard, and no answer to summarize.
	if state.parseStreak++; state.parseStreak == 2 {
		r.logger.Warn("tool calls repeatedly unparseable; nudging model to split the edit")
		r.nudge(ctx, req, state, parseFailureNudge, "tool calls repeatedly unparseable; asking model to split the edit")
	}
	return nudged || state.parseStreak >= 2
}

// finishOrRetry ends the request on a final answer and reports that it did.
// The one exception is an answer that ignores the work the thread did: the
// wrap-up nudge is appended instead and the loop takes one more turn.
func (r *Runner) finishOrRetry(ctx context.Context, req RunRequest, result turnResult, streamed bool, state *runState, started time.Time, turn int) (turnpkg.AgentResponse, bool) {
	if !result.done || r.nudgeSummary(ctx, req, state, result) || r.nudgeNoTools(ctx, req, state, result) {
		return turnpkg.AgentResponse{}, false
	}
	return r.finishTurn(ctx, req, result, streamed, *state, started, turn), true
}

// nudgeSummary catches the final answer that ignores the work the thread did:
// a thread that ran tools ending on a short pleasantry that names none of the
// tools or paths it touched ("Hey! What's up?", "Understood."). It appends one wrap-up
// nudge and reports that the turn should be retried; at most once per request,
// so the next answer is final whatever it says.
func (r *Runner) nudgeSummary(ctx context.Context, req RunRequest, state *runState, result turnResult) bool {
	answer := strings.TrimSpace(result.answer)
	// Never fire mid-work: a turn that made tool calls is still working.
	if result.toolCallCount > 0 || state.summaryNudged || state.callCount == 0 || answer == "" ||
		len(answer) >= maxTaskFreeAnswer || !taskFree(answer) || mentionsWork(answer, state.workTerms) {
		return false
	}
	state.summaryNudged = true
	r.logger.Warn("final answer names none of the work done; nudging for a summary", zap.Int("answer_len", len(answer)))
	r.nudge(ctx, req, state, summaryNudge, "nudging for a task summary")
	return true
}

// nudge appends an internal user message for the next turn and tells the UI why.
func (r *Runner) nudge(ctx context.Context, req RunRequest, state *runState, message, notice string) {
	state.messages, state.nudgeAppended = append(state.messages, inference.UserMessage(message)), true
	events.Notice(ctx, r.events, req.SessionID, r.parentSessionID, notice)
}

// noToolNudge is sent when the model answers a repository task without
// having called a single tool: the early give-up shape from #442, where a
// prompt naming a file or a build command is answered from the model's priors.
const noToolNudge = internalNudgePrefix + "Before answering the request above, look at the repository: no tool has run yet, so read the files or run the command it names first. A copied tool-count footer is history metadata, not an executed tool call."

// Match the observed unexecuted-action reply, not examples quoting the marker.
var copiedToolCount = regexp.MustCompile(`(?i)^(?:(?:i'll|i will|let me) (?:inspect|read|check|run)\b|(?:inspecting|reading|checking|running)\b)[^\n]*\. \[Tool calls this turn: [1-9][0-9]*\]$`)

// investigableMarkers are the commands whose presence in the prompt makes it a
// task about this repository rather than a question the model may answer from
// what it already knows.
var investigableMarkers = []string{"go test", "go build", "npm", "pytest", "cargo", "make "}

func needsInvestigation(prompt string) bool {
	low := strings.ToLower(prompt)
	for _, marker := range investigableMarkers {
		if strings.Contains(low, marker) {
			return true
		}
	}
	for _, field := range strings.FieldsFunc(prompt, notTermRune) {
		if len(field) > 3 && strings.ContainsAny(field, "./") {
			return true
		}
	}
	return false
}

// nudgeNoTools catches the run that answers such a prompt with zero tool calls.
// One ordinary nudge, plus one retry for copied positive tool-count metadata.
// Questions and requests that already ran tools do not need this recovery.
func (r *Runner) nudgeNoTools(ctx context.Context, req RunRequest, state *runState, result turnResult) bool {
	answer := strings.TrimSpace(result.answer)
	if state.noToolNudges >= 2 || (state.noToolNudges == 1 && !copiedToolCount.MatchString(answer)) ||
		state.callCount > 0 || result.toolCallCount > 0 || answer == "" ||
		strings.HasSuffix(answer, "?") || !needsInvestigation(req.Message) {
		return false
	}
	state.noToolNudges++
	r.logger.Warn("final answer ran no tools on a repository task; nudging to inspect first")
	r.nudge(ctx, req, state, noToolNudge, "nudging to inspect the repository before answering")
	return true
}

// taskFreeMarkers are what the observed dead-end answers are made of: a
// greeting, a self-introduction, or a bare acknowledgement. Matching on them
// rather than on shortness alone keeps a terse but real answer ("done",
// "patched and the tests pass") out of the nudge.
var taskFreeMarkers = []string{
	"hey", "hi", "hello", "what s up", "how can i help", "how may i help",
	"ready to help", "i am", "i m", "your friendly", "assistant",
	"understood", "anything else", "let me know",
	"happy to help", "tell me more", "how can i assist", "what would you like",
	"what can i do for you", "what are you working on",
}

func taskFree(answer string) bool {
	words := strings.FieldsFunc(strings.ToLower(answer), func(r rune) bool { return !wordRune(r) })
	joined := " " + strings.Join(words, " ") + " "
	return slices.ContainsFunc(taskFreeMarkers, func(marker string) bool {
		return strings.Contains(joined, " "+marker+" ")
	})
}

func appendWorkTerms(terms []string, calls []toolCallSignature) []string {
	for _, call := range calls {
		if len(terms) >= maxWorkTerms {
			return terms
		}
		terms = append(terms, call.Name)
		for _, field := range strings.FieldsFunc(call.Args, notTermRune) {
			if len(field) > 3 && strings.ContainsAny(field, "./") && len(terms) < maxWorkTerms {
				terms = append(terms, field)
			}
		}
	}
	return terms
}

func notTermRune(r rune) bool { return !wordRune(r) && !strings.ContainsRune("./_-", r) }

func wordRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// mentionsWork reports whether the answer names any of the terms, by full token
// or by its last path segment: the model writes "policy.go" for a path it was
// handed in full.
func mentionsWork(answer string, terms []string) bool {
	low := strings.ToLower(answer)
	return slices.ContainsFunc(terms, func(term string) bool {
		term = strings.ToLower(term)
		return strings.Contains(low, term) || strings.Contains(low, path.Base(term))
	})
}

// warnNearExhaustion appends the one-shot wrap-up warning when this request is
// within preExhaustionTurns of the turn ceiling or requestWarningPercent of the
// token budget. It reuses the repeat nudge's transcript handling, so the line is
// dropped from history once the model has seen it, and stands down when a repeat
// nudge already claimed this turn's trailing slot.
func (r *Runner) warnNearExhaustion(turn int, state *runState, started time.Time, budget *turnBudget) {
	remaining := budget.remaining()
	if state.warned || state.nudgeAppended || remaining <= 0 {
		return
	}
	warning := fmt.Sprintf(preExhaustionWarning, remaining)
	switch {
	case remaining <= preExhaustionTurns, r.spend.nearRequestLimit(state.usage):
	case r.timeNearlySpent(started):
		warning = preExhaustionTimeWarning
	default:
		return
	}
	state.warned = true
	r.logger.Warn("budget nearly spent; asking model to wrap up", zap.Int("turn", turn), zap.Int("turns_left", remaining), zap.Duration("elapsed", time.Since(started)))
	state.messages, state.nudgeAppended = append(state.messages, inference.UserMessage(warning)), true
}

func (r *Runner) timeBudgetSpent(started time.Time) bool {
	return r.settings.RunTimeout > 0 && time.Since(started) >= r.settings.RunTimeout
}

func (r *Runner) withRunDeadline(ctx context.Context, started time.Time, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, started.Add(budget))
}

// cutByTimeBudget tells a completion that died because the run deadline
// passed from one whose caller went away: the budget is spent and the
// caller's ctx is still live. Only the former is salvageable.
func (r *Runner) cutByTimeBudget(ctx context.Context, started time.Time) (string, bool) {
	if ctx.Err() != nil || !r.timeBudgetSpent(started) {
		return "", false
	}
	return fmt.Sprintf("time budget reached (%s) during a completion", r.settings.RunTimeout), true
}

func (r *Runner) timeNearlySpent(started time.Time) bool {
	return r.settings.RunTimeout > 0 && time.Since(started)*100 >= r.settings.RunTimeout*timeWarningPercent
}

func (r *Runner) stopAfterTurn(state *runState, completion completionOutput, result turnResult, started time.Time) (string, bool) {
	if r.timeBudgetSpent(started) {
		return fmt.Sprintf("time budget reached (%s)", r.settings.RunTimeout), true
	}
	if reason, stop := lengthRetryExhausted(completion, result); stop {
		r.logger.Warn("ending run after length retry did not pay off", zap.String("reason", reason))
		return reason, true
	}
	if reason := r.spend.exceeded(state.usage); reason != "" {
		return reason, true
	}
	return "", false
}

// lengthRetryExhausted ends a turn whose length retries did not produce a
// usable call. A tool that ran and failed remains an ordinary recoverable turn.
func lengthRetryExhausted(completion completionOutput, result turnResult) (string, bool) {
	if completion.lengthExhausted {
		return "output limit reached after length retries", true
	}
	if completion.lengthRetried && !completion.parseRecovered && hasParseFailure(result.outcomes) {
		return "the tool call made after the length retry was unusable", true
	}
	return "", false
}

func (r *Runner) applyProviderContext() {
	window := r.providerContext.ContextWindowTokens()
	r.mu.Lock()
	r.usage = r.usage.WithContextWindowTokens(window)
	r.mu.Unlock()
}

func stripTrailingInternalNudge(messages []inference.Message) []inference.Message {
	if len(messages) == 0 {
		return messages
	}
	last := messages[len(messages)-1]
	if last.Role == inference.RoleUser && strings.HasPrefix(last.Content, internalNudgePrefix) {
		return messages[:len(messages)-1]
	}
	return messages
}

func (r *Runner) prepareAndStoreContext(ctx context.Context, sessionID string, messages []inference.Message, state contextWindowState) ([]inference.Message, contextWindowState) {
	messages, state = r.budget.Prepare(ctx, sessionID, messages, state)
	r.mu.Lock()
	r.contextState = state
	r.mu.Unlock()
	return messages, state
}

// updateProviderUsage calibrates the local estimator against the provider's
// reported prompt size. sent is the prompt as the gateway built it (system
// prompt and tool schemas included), which is what the scale is calibrated
// against; basis is the runner's own transcript, which is the basis
// contextTokens later measures growth in. Recording the baseline in the wrong
// basis made the pending prompt look like it had not grown at all until a tool
// result outweighed the system prompt, which is how a single big tool result
// jumped straight past 100% (#fix3).
func updateProviderUsage(state contextWindowState, usage turnpkg.TokenUsage, sent, basis []inference.Message) contextWindowState {
	if usage.Available && usage.LatestPromptTokens > 0 {
		rawEstimate := estimatedMessageTokens(sent)
		observedScale := float64(usage.LatestPromptTokens) / float64(max(1, rawEstimate))
		observedScale = min(4, max(0.25, observedScale))
		if state.tokenEstimateScale <= 0 {
			state.tokenEstimateScale = observedScale
		} else {
			state.tokenEstimateScale = state.tokenEstimateScale*0.75 + observedScale*0.25
		}
		state.providerPromptTokens = usage.LatestPromptTokens
		state.providerMessageTokens = calibratedMessageTokens(basis, state.tokenEstimateScale)
	}
	return state
}

func failureSignatureKey(outcomes []turnpkg.ToolOutcome) string {
	return errorOutcomeKey(outcomes, false, func(o turnpkg.ToolOutcome) string { return o.Tool + "\x00" + o.Detail })
}

// errorOutcomeKey joins the keys of this turn's failing outcomes into one
// order-independent signature, optionally dropping repeats.
func errorOutcomeKey(outcomes []turnpkg.ToolOutcome, dedup bool, key func(turnpkg.ToolOutcome) string) string {
	keys := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		if k := key(outcome); outcome.Kind == turnpkg.OutcomeKindError && (!dedup || !slices.Contains(keys, k)) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, "|")
}

// hasParseFailure reports whether a turn failed to produce a usable tool call
// at all. A tool that ran and returned an error result is an ordinary turn
// outcome the model can recover from, so only an unparseable call counts.
func hasParseFailure(outcomes []turnpkg.ToolOutcome) bool {
	return slices.ContainsFunc(outcomes, func(o turnpkg.ToolOutcome) bool {
		return o.Kind == turnpkg.OutcomeKindParseError
	})
}

// failureToolKey is the error-text-free companion to failureSignatureKey: the
// set of tools that failed this turn, so a model cycling through malformed
// arguments for one tool is still caught.
func failureToolKey(outcomes []turnpkg.ToolOutcome) string {
	return errorOutcomeKey(outcomes, true, func(o turnpkg.ToolOutcome) string { return o.Tool })
}

func (r *Runner) finishTurn(ctx context.Context, req RunRequest, result turnResult, streamed bool, state runState, started time.Time, turn int) turnpkg.AgentResponse {
	if !streamed {
		events.Delta(ctx, r.events, req.SessionID, r.parentSessionID, result.answer)
	}
	state.usage = r.spend.priced(state.usage.WithContextWindowTokens(r.providerContext.ContextWindowTokens()).RecordUsage())
	resp := r.tagModels(turnpkg.AgentResponse{Message: result.answer, Usage: state.usage, ToolFailures: state.failures, ToolCallCount: state.callCount, StopReason: result.stopReason})
	r.dispatchAfterResponse(ctx, req, resp)
	r.storeState(state)
	r.logger.Info("agent request completed", zap.Duration("duration", time.Since(started)), zap.Int("turns", turn+1), zap.Int("answer_len", len(result.answer)))
	return resp
}

// tagModels records the model the run asked for and the one the provider said
// it served, so callers report the model actually used rather than whatever
// config held at startup.
func (r *Runner) tagModels(resp turnpkg.AgentResponse) turnpkg.AgentResponse {
	if r.turnProcessor != nil {
		resp.ExecutionEvidence = r.turnProcessor.evidence.Snapshot()
		resp.ToolFlagged = turnpkg.FlaggedExecution(resp.ExecutionEvidence)
	}
	if gw, ok := r.modelGateway.(modelSwitcherGateway); ok {
		resp.Model = gw.Model()
	}
	if gw, ok := r.modelGateway.(servedModelGateway); ok {
		resp.ServedModel = gw.ServedModel()
	}
	if gw, ok := r.modelGateway.(retryCountingGateway); ok {
		resp.Retries = gw.RetryCounts()
	}
	return resp
}

func (r *Runner) applyPendingModel() {
	p := r.pendingModel.Load()
	if p == nil {
		return
	}
	if sw, ok := r.modelGateway.(modelSwitcherGateway); ok {
		sw.SetModel(*p)
		r.providerContext.SetModel(*p)
		r.refreshProviderMetadata(*p)
	}
	r.pendingModel.CompareAndSwap(p, nil)
}

func (r *Runner) answerOrFallback(result turnResult, turn int, completion completionOutput) string {
	if !result.done || result.answer != "" || result.toolCallCount != 0 {
		return result.answer
	}
	reason := emptyAnswerReason(turn, r.settings.MaxTurns, completion.finishReason)
	r.logger.Warn("model returned empty response after retry", zap.Int("turn", turn), zap.String("reason", reason))
	return fmt.Sprintf(constants.EmptyModelResponseFormat, reason)
}

// emptyAnswerReason distinguishes a blank final caused by the turn budget
// running out from one the provider simply ended, so the user is not left
// guessing which of the two they hit.
func emptyAnswerReason(turn, maxTurns int, finish inference.FinishReason) string {
	turn++ // the loop counter is zero-based; users count from one
	if maxTurns > 0 && turn >= maxTurns {
		return fmt.Sprintf("the turn budget is spent: %d of %d turns used", turn, maxTurns)
	}
	if finish != "" {
		return fmt.Sprintf("turn %d of %d, the provider finished with %q", turn, maxTurns, string(finish))
	}
	return fmt.Sprintf("turn %d of %d, the provider reported no finish reason", turn, maxTurns)
}

// failedResponse records accumulated usage and persists it to the runner
// before an error return, matching the success paths — otherwise the tokens
// burned by a failed request vanish from the runner's snapshot.
func (r *Runner) failedResponse(state runState) turnpkg.AgentResponse {
	state.messages = stripTrailingInternalNudge(state.messages)
	state.usage = r.spend.priced(state.usage.WithContextWindowTokens(r.providerContext.ContextWindowTokens()).RecordUsage())

	r.storeState(state)
	return r.tagModels(turnpkg.AgentResponse{Usage: state.usage, ToolFailures: state.failures, ToolCallCount: state.callCount})
}

func (r *Runner) storeState(state runState) {
	r.mu.Lock()
	r.usage, r.messages, r.contextState = state.usage, state.messages, state.contextState
	r.mu.Unlock()
}

// toolCallMarkers are the textual tool-call openers models fall back to when
// tools are disabled. They must never reach the user as an answer.
var toolCallMarkers = []string{"<|tool_call", "<tool_call", "<function="}

// stripToolCallMarkup drops everything from the first textual tool-call marker
// onwards, leaving only the prose the model wrote before it (possibly empty).
func stripToolCallMarkup(text string) string {
	cut := len(text)
	for _, marker := range toolCallMarkers {
		if i := strings.Index(text, marker); i >= 0 && i < cut {
			cut = i
		}
	}
	stripped := strings.TrimSpace(text[:cut])
	if cut == len(text) {
		return stripped
	}
	// What is left is the preamble of a call that was dropped ("Let me write
	// the availability JSON:"); returning it as the answer reports an intent
	// that never ran, so the dangling line goes with the call (#444).
	if !strings.HasSuffix(stripped, ":") {
		return stripped
	}
	if i := strings.LastIndexByte(stripped, '\n'); i >= 0 {
		return strings.TrimSpace(stripped[:i])
	}
	return ""
}

// gracefulExhaustion makes one final tool-free completion to salvage a best-effort
// answer when a budget (turns, session tokens, or session cost) runs out, instead
// of returning a bare error and discarding all partial progress. Falls back to the
// bare error if that call also fails. A reason from the repeat guard picks
// repeatExhaustionWrapUp over the generic exhaustionWrapUp: the repeated-tool-call
// path asks for a plain summary rather than framing it as a budget running out,
// since the work is often already done and the model was just looping — the
// notice and ledger still carry the original exhaustion reason either way.
// The salvage runs tool-free and thinking-free, like the parse give-up
// summary: a model that was looping or cut mid-output answers a "stop and
// summarize" ask far more reliably with its reasoning budget out of the way.
func (r *Runner) gracefulExhaustion(ctx context.Context, req RunRequest, state runState, started time.Time, reason string) (turnpkg.AgentResponse, error) {
	r.logger.Warn("agent budget exhausted; requesting best-effort answer", zap.String("reason", reason), zap.Int("max_turns", r.settings.MaxTurns))
	events.Notice(ctx, r.events, req.SessionID, r.parentSessionID, reason+"; requesting best-effort answer")
	wrapUp := exhaustionWrapUp
	if strings.HasPrefix(reason, repeatExhaustionReasonPrefix) {
		wrapUp = repeatExhaustionWrapUp
	}
	// The wrap-up gets its own slice of the wall clock, measured from now so
	// a budget that expired mid-turn still leaves it salvagePercent of
	// RunTimeout to answer in.
	salvageCtx, cancel := r.withRunDeadline(ctx, time.Now(), r.settings.RunTimeout*salvagePercent/100)
	defer cancel()
	state.messages, state.contextState = r.prepareAndStoreContext(salvageCtx, req.SessionID, state.messages, state.contextState)
	salvage := append(append([]inference.Message{}, state.messages...), inference.UserMessage(wrapUp))
	modelTurn, err := r.modelGateway.Continue(salvageCtx, ContinueModelRequest{SessionID: req.SessionID, Messages: salvage, DisableTools: true, DisableThinking: true})
	if err != nil {
		return r.failedResponse(state), fmt.Errorf("agent budget exhausted (%s): %w", reason, err)
	}

	// Parse like a normal reply (forgiving: plain text or tagged reply both yield
	// clean prose). Ignore any tool call the model emits — this is the give-up path.
	// A wrap-up cut off by the output limit is a severed sentence, not an
	// answer: fall through to the canned message rather than shipping it.
	answer := ""
	if parsed, perr := r.turnProcessor.syntax.ParseAssistantOutput(modelTurn.Completion.content); perr == nil && parsed.ToolCall == nil && !modelTurn.Completion.lengthExhausted {
		answer = stripToolCallMarkup(parsed.Reply)
	}
	if answer == "" {
		// Even when the salvage completion fails, the transcript's own ledger
		// of completed tool calls is worth more than a bare apology: the work
		// is often already on disk (#w1r3).
		answer = completedCallsNote(state.callCount) + "I wasn't able to finish: " + reason + ". Please simplify or rephrase the request."
	} else if code := exhaustionCode(reason); code != stopReasonRepeatLoop {
		// A salvaged answer reads as a confident completion — F1/1 and F1/8 of
		// the round-8 bench both claimed the task was done over a tree that
		// did not compile. Say so in the answer itself, not only in the ledger.
		answer = fmt.Sprintf(incompleteNotice, reason) + "\n\n" + answer
	}
	events.Delta(ctx, r.events, req.SessionID, r.parentSessionID, answer)
	state.usage = r.spend.priced(state.usage.AccumulateTurnUsage(modelTurn.Completion.usage).
		WithContextWindowTokens(r.providerContext.ContextWindowTokens()).RecordUsage())
	state.contextState = updateProviderUsage(state.contextState, modelTurn.Completion.usage, modelTurn.Messages, state.messages)
	resp := r.tagModels(turnpkg.AgentResponse{Message: answer, Usage: state.usage, ToolFailures: state.failures, ToolCallCount: state.callCount, StopReason: exhaustionCode(reason)})
	r.dispatchAfterResponse(ctx, req, resp)
	// Copy rather than append in place: the salvage call's slice still holds
	// the wrap-up instruction, and growing over it would rewrite the messages
	// the gateway was handed.
	kept := stripTrailingInternalNudge(modelTurn.Messages)
	state.messages = append(append([]inference.Message{}, kept...), inference.AssistantMessage(answer))
	r.storeState(state)
	r.logger.Info("agent request completed via best-effort answer", zap.Duration("duration", time.Since(started)), zap.Int("answer_len", len(answer)))
	return resp, nil
}

// Stop reasons for a run that ended on a budget rather than on its own answer.
const (
	stopReasonStepLimit  = "step_limit"
	stopReasonRepeatLoop = "repeat_loop"
	stopReasonTimeBudget = "time_budget"
	stopReasonBudget     = "budget_exhausted"
	// stopReasonProviderError marks a run the provider ended for us: the
	// canned give-up answer reads exactly like a real one, so the ledger has
	// to say it was not (#539, ADR 0001).
	stopReasonProviderError = "provider_error"
)

// incompleteNotice opens a best-effort answer, so a reader (and the acceptance
// scorer) sees the run was cut short without parsing the ledger.
const incompleteNotice = "\u26a0 %s: work may be incomplete"

func stepLimitReason(maxTurns int) string {
	if maxTurns == 1 {
		return "step limit reached (1 turn)"
	}
	return fmt.Sprintf("step limit reached (%d turns)", maxTurns)
}

// exhaustionCode turns gracefulExhaustion's prose reason into the machine
// -readable stop reason the ledger carries (ADR 0001).
func exhaustionCode(reason string) string {
	switch {
	case strings.HasPrefix(reason, "step limit reached"):
		return stopReasonStepLimit
	case strings.HasPrefix(reason, repeatExhaustionReasonPrefix):
		return stopReasonRepeatLoop
	case strings.Contains(reason, "time"):
		return stopReasonTimeBudget
	}
	return stopReasonBudget
}

func (r *Runner) dispatchAfterResponse(ctx context.Context, req RunRequest, resp turnpkg.AgentResponse) {
	r.events.Dispatch(ctx, events.Event{
		Type: events.EventAfterResponse, Timestamp: time.Now(), SessionID: req.SessionID, ParentSessionID: r.parentSessionID,
		Response: &events.ResponsePayload{Query: req.Message, Text: resp.Message, ToolOutcomes: resp.ToolFailures, ToolCallCount: resp.ToolCallCount, ExecutionEvidence: resp.ExecutionEvidence},
	})
}

func turnSignatureKey(calls []toolCallSignature) string {
	// The result hash is part of the identity: an identical call that returns
	// different output (a rebuild after an edit) is verification, not a loop.
	keys := core.MapSlice(calls, func(c toolCallSignature) string { return c.Key + c.Result })
	// Sort so the signature is independent of the order the model returns
	// parallel tool calls in; the same set across turns must hash identically.
	sort.Strings(keys)
	return strings.Join(keys, "|")
}

func (r *Runner) nextModelTurn(ctx context.Context, turn int, req RunRequest, messages []inference.Message, completedCalls int) (ModelTurn, error) {
	if turn == 0 {
		return r.modelGateway.Begin(ctx, BeginModelRequest{SessionID: req.SessionID, Message: req.Message, Images: req.Images, History: messages})
	}
	return r.modelGateway.Continue(ctx, ContinueModelRequest{SessionID: req.SessionID, Messages: messages, CompletedCalls: completedCalls})
}
