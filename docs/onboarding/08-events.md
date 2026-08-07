# 08 — Events & Audit

Package: `platform/events/`.

## The bus

One implementation: `OrderedDispatcher`. `Register(handler)` returns an
unregister func; `Dispatch(ctx, event)` runs handlers **synchronously, in
registration order**, on the caller's goroutine. Panics are recovered and
logged; handler errors are logged and do not stop later handlers; a cancelled
ctx stops the fan-out. There is no async dispatcher — a handler that must do
slow work owns its own worker (the memory curator is the template: bounded
queue, drop-on-full, drain-on-close).

`events.Bus` = `Dispatcher` + `Register`. Most producers only need
`Dispatcher`.

## Event families (`events.go`)

| Type | Payload | Producers → consumers |
|---|---|---|
| `before_prompt` / `after_prompt` | `PromptPayload` (request, blocks) | prompt builder → (observability) |
| `after_response` | `ResponsePayload` (query, text, tool outcomes) | runner → memory curator |
| `tool_started/completed/failed` | `ToolPayload` (id, name, args, duration, bytes, code, error) | turn processor → TUI blocks, exec JSONL |
| `notice` | text | compaction, gateway retries, recall gate, jobs → UI status lines |
| `assistant_delta` | text | streaming + final answers → UI |
| `audit` | `AuditPayload` (action, tool, paths, outcome, details map) | every sensitive tool → (log/ledger) |
| `toolbox_refresh_requested` | — | tool_search, create_tool → toolbox rescan |
| `before/after_recall`, `before/after_save` | recall/save payloads | memory manager → (observability) |

`Event.SessionID` scopes events to a session; `ParentSessionID` marks subagent
traffic (the TUI filters to its own session; the curator skips subagent turns).

## Audit convention

Sensitive tools emit exactly one audit event per invocation via
`core.DispatchAudit(ctx, dispatcher, payload, err)` — it fills `SessionID`
from ctx and `Error` from err. Query text is never logged raw; use
`core.HashHexTrimmed` (see file_search/web_search). Include `policy_tier`,
counts, and `duration_ms` in `Details`.

## The TUI threading rule (important)

Bubble Tea is single-threaded; bus handlers run on arbitrary goroutines.
`cmd/agentui/events.go` bridges them: the handler pushes into the
mutex-guarded `toolEventQueue` and wakes the program with
`program.Send(toolEventsReadyMsg{})`; the model drains the queue inside
`Update`. **Never** touch model state directly from a handler. `exec` has the
same shape but writes JSONL/stderr directly (safe: no UI state).

## Ordering guarantees you can rely on

- `tool_started` always precedes its `tool_completed`/`tool_failed` (same
  goroutine per call), but events from *parallel* tool calls interleave.
- `after_response` fires before `Send` returns (same call stack), so memory
  writes for turn N are enqueued before turn N+1 begins — though background
  summarization completes asynchronously.
- Handlers registered earlier see events earlier; the memory handler is
  registered at bootstrap before any session runs.
