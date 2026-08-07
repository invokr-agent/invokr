# 01 — The Big Picture

Invokr is a single Go binary that drives an OpenAI-compatible LLM in a bounded
tool-use loop, with a terminal UI, persistent memory, and a hardened tool
execution surface.

## Layer map

```text
main.go                       setup preflight → config load → sandbox re-exec? → cobra
└─ cmd/                       CLI commands; cmd/agentui/ is the Bubble Tea TUI
   └─ platform/runtime/       composition root (bootstrap.go wires everything)
      ├─ agent/               the turn loop: Session → Runner → Gateway → TurnProcessor
      ├─ clients/             OpenAI-compatible HTTP client
      ├─ tools/               tool registry (Caller) + tools/system/* native tools
      ├─ capability/          memory, approval, syntax, skills, agentdef,
      │                       jobs, sandbox, sessionstore — feature verticals
      ├─ platform/events      ordered synchronous event bus
      ├─ platform/turn        shared session/response protocol types
      └─ surface/             messaging transports (telegram/signal), ui styles
```

Dependency direction: `cmd` → `platform/runtime` → everything else.
`config/`, `constants/`, `platform/turn/`, `support/fsutil/` are leaf packages
everyone may import. `capability/*` packages must not import `platform/runtime`
(the one exception is `capability/jobs/jobrun`, which exists precisely to hold
that import without a cycle).

## Life of a message

1. **Input.** The TUI (`cmd/agentui/model.go` `submit`) or plain mode calls
   `turn.Session.Send(ctx, text)` — implemented by `agent.Session`.
2. **Prompt.** On turn 0 the gateway asks `agentprompt.Builder` for a fresh
   system prompt: base prompt + syntax rules + skill list + tool list + fenced
   memory blocks (from `memory.Manager.PromptBlocks`, char-budgeted). The
   optional recall gate may add a "Relevant Memory" block on a session's first
   message.
3. **Compaction check.** `contextBudget.Prepare` estimates tokens; at 70% of
   the window it warns and prefetches a conversation summary, at 85% it
   replaces the middle of the conversation with that summary (fallback: elide
   old tool output). ADR 0003.
4. **Completion.** `OpenAIModelGateway` sends the request through
   `clients.Client` (streaming, stall watchdog). Plain-text reply deltas are
   streamed to the UI; JSON/tool-call output is not.
5. **Interpretation.** `TurnProcessor.Process` looks at the completion:
   - native tool calls → executed concurrently (max 4), results reassembled in
     call order, each guarded by approval and panic recovery;
   - tagged-JSON (`<|tool_call>` blocks / `{"type":"reply",…}`) for providers
     without native tool calling;
   - a final reply ends the loop.
6. **Tools.** `tools.Caller.HandleCall` resolves the handler, enforces the
   per-session deferred-schema rule, runs the tool, and filters the result
   (tokf). Failures become structured
   payloads the model sees; denials are remembered for the rest of the request.
7. **Loop.** The runner repeats up to `agent.max_turns` (default 40). A repeat
   guard nudges the model if it issues identical tool calls three turns
   running. `agent.max_request_tokens` bounds the same loop by work done,
   estimated locally when the provider reports no usage. At either limit, one
   final tool-free completion produces a best-effort answer.
8. **After response.** An `EventAfterResponse` fires. The memory curator
   appends the turn to raw memory events and the JSONL transcript, curates
   pinned markdown, and every 12th event summarizes the batch into durable
   memory records (on a background worker). The TUI renders the answer plus an
   authoritative tool-outcome ledger if anything failed.

## Frontends over the same session

- `invokr agent` — TUI (or plain stdin mode when stdin isn't a terminal).
- `invokr exec` — one shot; JSONL event ledger with `--json`; approvals
  auto-denied unless `--allow`/`--allow-all`.
- `invokr bot serve` — Telegram/Signal, one worker+session per chat.
- `invokr jobs run <id>` / cron — headless runs with a fixed approval policy.

All of them go through the same `platform/runtime.Bootstrap` and the same
`agent.Session`; only approval handling and event rendering differ.

## The three trust boundaries

1. **Model ↔ runtime**: everything the model requests is a `ToolCall`;
   policy (workspace roots, shell tiers, approvals) is enforced in the runtime,
   never by prompt text alone.
2. **Runtime ↔ tool processes**: toolbox plugins run in a Linux namespace
   sandbox with read-only binds and no network by default; Python installs go
   through an allowlist proxy with pinned, hash-locked, cooldown-checked wheels.
3. **Agent ↔ host** (optional): the whole process can re-exec inside docker
   before bootstrap. This *replaces* the per-tool sandbox (ADR 0005).
