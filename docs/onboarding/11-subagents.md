# 11 — Subagents

Packages: `tools/system/delegate/` (the tool), `capability/agentdef/`
(definitions), `platform/runtime/subagent.go` + `filtered_caller.go` (the
runner).

## delegate_task

The model calls `delegate_task` with 1–6 tasks (`label`, `prompt`, optional
`context`, `timeout_seconds`, `agent`), `max_parallel` (default 3, cap 6), and
`max_turns` (default 4). Always approval-gated. Tasks run concurrently under a
semaphore; each result is `{label, final_text, stop_reason, error, usage}` —
only final summaries come back, never child transcripts.

Guard rails:

- **Depth cap 1** — a delegate depth counter rides the context
  (`core.WithDelegateDepth`); subagents cannot delegate further.
- **Memory isolation** — child sessions get `memory.NewNoopManager()`; the
  parent records each task's input/output as an episode record itself
  (`recordDelegateMemory`, serialized by a mutex because the manager may be
  SQLite-backed).
- **Timeouts** — per-task `timeout_seconds` becomes a child context deadline;
  a deadline produces `stop_reason: "timeout"` while preserving usage.
- Labels must be unique; unknown `agent` names fail fast listing the
  available definitions.

## Agent definitions (`~/.invokr/agents/<name>.md`)

```markdown
---
name: reviewer            # required, [a-z][a-z0-9-]*
description: Read-only reviewer.   # required
model: gpt-4o-mini        # optional; inherit if omitted
max_turns: 4              # optional; else the tool's max_turns
tools:                    # optional; omit = full tool set
  - file_search
---
Markdown body = system-prompt addition (required).
```

`agentdef.NewRegistryFromDir` loads at bootstrap; per-file errors are
accumulated (name collisions rejected). The registry is advisory-audited at
startup: allowlist entries naming unregistered tools log a warning.

## How a definition is applied (`subagent.go`)

`RunSubagent` builds a fresh `agent.NewSession` sharing the parent's provider,
events, skills, and syntax, with:

- settings overridden per definition (`applyDefinition`): model, max_turns,
  and instructions appended to the base system prompt;
- `ParentSessionID` set (so the TUI filters child events and the curator skips
  child turns);
- the tool caller wrapped in `filteredCaller` when `tools:` was explicit —
  specs, prompt specs, callable specs, and `HandleCall` are all filtered;
  a blocked call returns a flagged `not_allowed` ToolError. Dynamic
  registration through a filtered caller is a deliberate no-op (a subagent
  must not mutate the parent's tool set).

An explicit-but-empty `tools:` list means **no tools** — `ToolsExplicit`
distinguishes "absent" from "empty".

## Definition errors are results, not crashes

`RunSubagent` returns definition/allowlist problems as a
`SubagentResult{StopReason:"error"}` with a nil error so one bad task doesn't
poison the batch; only session-construction/send failures propagate as errors
(and `runOneTask` normalizes those per task too).
