# 02 — The Agent Loop

Packages: `agent/`, `capability/agentprompt/`, `capability/syntax/`.

## Cast of characters

- **`agent.Session`** (`session.go`) — public facade. `Send` runs one user
  request; also exposes `Usage()`, `SetModel` (applied at the next turn
  boundary via an atomic pointer), `RequestCompaction`, `ResumeTranscript`.
- **`Runner`** (`runner.go`) — owns the bounded loop, the outcome ledger, the
  repeat guard, and the conversation state (`messages`,
  `contextWindowState`) under a `sync.RWMutex`.
- **`OpenAIModelGateway`** (`model_gateway.go`, `structs.go`) — builds
  requests, retries two failure shapes, streams deltas.
- **`TurnProcessor`** (`session_tools.go`) — interprets one completion:
  executes tools, emits tool events, returns `turnResult`.
- **`contextBudget`** (`context_budget.go`) — compaction state machine.
- **`agentprompt.Builder`** — assembles the system prompt each turn 0.
- **`syntax.TaggedJSONSyntax`** — the non-native tool-call protocol.

## Runner.Run, step by step

```go
for turn := 0; turn < MaxTurns; turn++ {
    applyPendingModel()                          // /model switch lands here
    messages = budget.Prepare(...)               // maybe compact
    modelTurn = gateway.Begin/Continue(...)      // Begin only on turn 0
    result, messages = turnProcessor.Process(...)
    // accumulate usage, outcomes, call count
    if result.done { return finishTurn(...) }
    if guard.observe(...) { append repeatedToolCallNudge }
}
return gracefulExhaustion(...)                   // one tool-free completion
```

Details that matter:

- **Outcome ledger (ADR 0001).** Every tool error/denial/parse failure becomes
  a `turn.ToolOutcome`. UIs append an "[Authoritative tool outcome]" block via
  `turn.AppendToolOutcomeGuard` so model prose can't misreport what happened.
- **Repeat guard.** Fires after 3 identical tool-call turn signatures, an
  A/B/A/B alternation, or 3 identical failure signatures; appends a
  user-role nudge prefixed `[invokr internal retry]`. The nudge is stripped
  from history the next turn (or before returning) so it never pollutes
  transcripts or compaction summaries.
- **Graceful exhaustion.** At the turn limit the runner makes one
  `DisableTools` completion and salvages a best-effort answer instead of
  erroring out.
- **Failure paths persist usage.** `failedResponse` still records token usage
  so failed requests don't vanish from the session totals.

## Gateway retries (request-local, never persisted)

`complete()` loops with two independent single-retry nudges, then truncates
`messages` back to the original length so successful retries leave no trace:

1. **Tool-call parse failure** — llama.cpp 500s with "Failed to parse tool call
   arguments…" when a small model runs past the token cap mid-JSON. Classified
   via `clients.ErrToolCallParseFailed`; one retry nudge, then a synthetic
   readable failure answer (`syntheticFailure`).
2. **Empty completion** — no text, no tool calls; one "answer the question"
   nudge.

## Streaming rules (`safeDeltaEmitter`)

Deltas stream to the UI only when the output is a plain-text reply, or the
tagged-JSON reply form (it incrementally decodes the JSON string body,
handling split escapes and surrogate pairs). Output starting with `{` or `<`
that isn't the reply prefix is blocked — tool calls never stream. If nothing
streamed, the runner dispatches the final answer as one delta at the end.

## Context compaction (ADR 0003)

- Token estimate: bytes/4, scale-calibrated against provider-reported prompt
  tokens each turn (`updateProviderUsage`, EWMA of observed scale, clamped
  0.25–4).
- At `context_warning_percent` (70): send a notice, prefetch a summary in the
  background (keyed by segment hash, reused if history unchanged).
- At `context_compact_percent` (85) or `/compact`: keep messages[0:2] (system +
  first user), insert `"Conversation summary:\n…"` as a *user* message (some
  provider templates only allow system at position 0), keep the last 4
  messages.
- If summarization fails: fall back to eliding old tool outputs (then tool-call
  arguments) until under budget.
- The summarizer is `SummarizeConversation` on the gateway — a one-shot
  completion using `agent.summarizer_model` if configured, else the chat model.

## Prompt building (`capability/agentprompt`)

`Builder.Build` (turn 0 only — later turns reuse history but the leading system
message is rebuilt fresh, see `initialMessages`):

1. `memory.PromptBlocks` (char budget 6000) — fires before/after prompt events.
2. Recall-gate block if enabled and first turn.
3. System prompt: base → syntax response rules → skill list (names +
   one-liners; "use load_skill first") → tool list (native mode: only deferred
   tools listed; tagged mode: every tool with example) → memory fenced in
   `<recalled-memory>` with an anti-injection preamble.

## Tagged-JSON syntax (`capability/syntax`)

For providers without native tool calling (`agent.native_tool_calling=false`):

- Reply: `{"type":"reply","message":"..."}`
- Tool call: `<|tool_call>\n{"name":...,"arguments":{...}}\n<|tool_call|>`
- Tool result goes back as a *user* message `{"type":"tool_result",...}`.
- Parsing is forgiving: unparseable output becomes the reply, but a reply
  containing literal tool-call markup outside code fences is bounced back as
  an error (the model meant to call a tool).
- Denials get `DeniedPayload` with `"denied":true` + guidance so the model
  stops re-requesting; an identical retry inside the same request is refused
  without re-prompting the user.

## Gotchas

- `ResumeTranscript` restores text plus the persisted tool outcome ledger
  (ADR 0001); denials and loaded schemas are still gone. It refuses if the
  session already has history.
- Both the runner and the gateway assume `messages[0]` is the system message
  and `messages[1]` the first user message; compaction preserves that shape.
- Parallel native calls: results are written to distinct slice indices —
  don't add shared mutable state to `processNativeCall` without a lock.
