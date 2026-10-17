# Invokr Subsystems

## Agent

`agent.Session` is the user-facing facade. `Runner` owns the bounded turn loop. `ModelGateway` owns model request/response transport. `TurnProcessor` interprets model output, executes tools, emits tool events, and decides final reply vs tool continuation.

Key files: `agent/session.go`, `agent/runner.go`, `agent/model_gateway.go`, `agent/session_tools.go`.

Execution evidence comes from native execution owners before output filtering.
Foreground read-only calls preserve earlier observations; later writes mark them
stale and concurrent writes make them uncertain. Starting persistent work latches
uncertainty for the rest of that session, including after request reset; stopping
the process does not clear the conservative latch. Delegated failures and flags
reach the parent, and chat displays the same guards as CLI/TUI. Disk keeps the
full ledger; resumed prompts use historical outcome counts and a bounded last
command. Evidence never establishes full task verification.

Shell preserves Bash's default control flow. An EXIT trap inspects only the final
pipeline and reports its first nonzero status other than SIGPIPE (141). Earlier
pipelines followed by successful commands are not proof of success; the ledger
records process observations, not test assertions. Standalone exit 141 remains a
failure. Explicit traps can override this behavior. `exec --fail-on-tool-error`
includes flagged results as well as failures and denials; ordinary exec keeps its
existing exit policy. Child summaries carry bounded historical evidence.

## Provider

`clients.LLMProvider` is the provider seam. `clients.Client` uses the inference-router adapter for OpenAI-compatible servers, normalizes base URLs to `/v1`, streams completions with a stall watchdog, and queries provider metadata from `/props`. Memory *embeddings* run on the in-process Zerfoo model runtime (`capability/memory/zerfoo/`); memory *summarization* runs on the main chat provider via `capability/memory/chat_summarizer.go` (Zerfoo CPU decode is too slow — see docs/memory.md).

Key files: `clients/llm_provider.go`, `clients/openai.go`.

## Tools

`tools.Caller` is the central registry. It holds specs, native handlers, session-loaded schema state, and optional result filtering.

Tool-call routing:

1. a first call to a deferred tool (schema not loaded for the session) loads it for the next turn
2. call native handler if registered
3. return unknown-tool error

Native tools are loaded by `tools/system/load_system_tools.go`, including search,
patching, memory, delegation, shell, code interpreter, tmux, toolbox discovery, and
agent-authored tool creation. The tmux tool uses a process-private socket, runs its server inside a namespace sandbox that shares the host network (ADR 0018), confines
session working directories through workspace policy, and approval-gates state-changing
open/send actions.

Toolbox executable tools live under `toolbox/<tool_name>/` with `tool.json`, `plugin.json`, or `hamr.json`, plus exactly one runtime entrypoint: `main.go`, `main.js`, or `main.py`. `tools/system/toolbox` registers them as source-scoped native handlers. Tools receive JSON on stdin and return plain text or `{ "result": ..., "error": ... }`.

## Workspace And Shell

Workspace policy normalizes configured roots and rejects paths outside allowed roots. Hidden paths are separately controlled. Workspace-aware native tools should use `tools/system/workspace.Policy`.

Shell is a native tool, not a toolbox plugin. The model sends a bash line; `policy.go` parses it (mvdan) and checks every command root against the executable allowlist and the read/write/network tier rules, then `exec.go` runs it as `bash -c` inside the namespace sandbox (workspace roots writable, no network below the network tier). Explicit config for write and network tiers, timeout/output truncation, and audit events are unchanged.

## Memory

`capability/memory.Manager` exposes prompt blocks, save/update/delete/list, and recall.

Storage/recall forms:

- pinned Markdown memory for stable user/project/agent facts
- SQLite records with FTS5 for runtime memory and lexical recall
- optional vector recall through built-in Zerfoo embeddings and SQLite-stored vectors

`TieredManager` coordinates prompt memory, CRUD, recall, after-response event handling (delegated to a `curator` with a background summarization worker), raw memory event capture, chat-model summarization, and optional vector storage. Recall is delegated to an orchestrator that combines pinned and lexical/vector sources under policy, with a raw-turn fallback when no records match yet.

Two opt-in extensions, both default-off:

- **Forgetting** (`capability/memory/forgetting/`) — Ebbinghaus-style decay: records lose retention over time, are reinforced on recall, and drop into a cold archive when retention falls below threshold. The `recall_archive` tool is the only reader of archived records and always requires approval.
- **Recall gate** (`capability/memory/recallgate/`) — a cheap "nothink" probe completion on the first turn of a session that decides whether long-term recall would help and, if so, injects a "Relevant Memory" prompt block. Every failure mode degrades to "no block".

## Approvals

`capability/approval.Broker` is the request/decision seam tools use to ask for
permission. `ApprovalQueue` sits in front of it for the interactive UI: when
parallel tool calls each raise an approval, the requests accumulate as a batch.
The TUI panel (`approval queue (N pending)`) lets the user decide per-item
(`a`/`d`) or all at once (`A` approve-all, `D`/`esc` deny-all). `DecideAll`
resolves every pending request with one decision. In non-interactive `exec`, an
approval request is auto-denied.

Key files: `capability/approval/broker.go`, `capability/approval/queue.go`, `cmd/agentui/model.go` (queue keys).

## Subagents

`delegate_task` (`tools/system/delegate/`) runs bounded child agent sessions in
isolation and returns only their final summaries. Depth is capped at 1 (subagents
cannot delegate further). A task may name an `agent` — a definition loaded from
`~/.invokr/agents/<name>.md` by `capability/agentdef`. A definition supplies a
system-prompt addition, and optionally a model, `max_turns`, and an explicit tool
allowlist. `platform/runtime/subagent.go` applies the definition: instructions are
appended to the subagent's system prompt, and an explicit allowlist wraps the tool
caller in a filtered caller so the child only sees permitted tools. An unknown
agent name is rejected before any child runs, with an error listing available names.

Key files: `capability/agentdef/agentdef.go`, `platform/runtime/subagent.go`, `platform/runtime/filtered_caller.go`, `tools/system/delegate/delegate.go`.

## Sandbox

When `sandbox.enabled` is true and `INVOKR_IN_SANDBOX` is unset, the process
re-execs itself inside a docker/podman container before `Bootstrap` runs
(`capability/docker_sandbox/relaunch.go`). The image is auto-built from the current binary
unless `sandbox.image` pins one; `sandbox.engine` selects `docker` (default) or
`podman`. This is whole-agent isolation, distinct from the per-toolbox-tool
sandbox in `tools/system/toolbox/` (ADR 0005).

## Messaging

`invokr bot serve` runs `surface/messaging.Router`, which fans inbound messages
from every configured transport into one loop and dispatches them to per-chat
agent sessions. Transports are Telegram (`surface/messaging/telegram/`, long-poll
`getUpdates`) and Signal (`surface/messaging/signal/`, signal-cli JSON-RPC over a
UNIX socket). Allowlists (`allowed_chat_ids` / `allowed_numbers`) gate who may talk
to the agent; a chat→session store maps each conversation to a persisted session.
Approvals over chat are handled by a YES/NO reply convention.

## Scheduled jobs

`capability/jobs` owns a job store and the crontab boundary; `capability/jobs/jobrun`
executes a due job by constructing a runtime (kept in a separate package to avoid
an import cycle with `tools/system/schedule`). The `jobs` CLI and the agent-facing
`schedule` tool are two front ends over the same store.

## Config

`config.AppConfig` owns provider, agent, log, memory (incl. zerfoo, recall gate,
forgetting), workspace, toolbox, tools (the advertised tool set), skills, agents
(subagent dir), shell, web search, code interpreter, MCP, sandbox, messaging,
approvals, LSP, and UI settings. `LoadFile` resolves defaults -> file -> environment;
`WithDefaults()` is env-free and idempotent. Cobra then binds flags onto the resolved
config, giving flags highest precedence. Messaging config lives in `config/messaging.go`.

Avoid adding config unless behavior must be operator-tunable.

## Skills

Skills are filesystem-discovered directories containing `SKILL.md`. The loader supports YAML front matter, validates name/description/body, caches loaded skills, and uses `singleflight` for duplicate concurrent loads.

## UI

`cmd/agentui` supports interactive Bubble Tea mode and plain stdin mode. The model owns chat viewport, input, spinner, keymap, messages, history navigation, log panel state, and tool event display. Runtime services are injected from `platform/runtime`.
