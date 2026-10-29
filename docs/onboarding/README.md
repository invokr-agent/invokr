# Invokr Onboarding

Everything you need to go from zero to a productive change. Read
`01-big-picture.md` first, then the guide for the subsystem you're touching.
Prefer pictures? [16-diagrams.md](16-diagrams.md) walks the same ground
visually, from system context down to subsystem internals.
These guides describe *how the code actually works today* — file paths and type
names are real, verified 2026-07-17.

## Day 1

```bash
go run main.go setup           # creates ~/.invokr (config, toolbox, skills, memory files)
go run main.go agent           # interactive TUI against http://localhost:8080 (OpenAI-compatible)
go run main.go exec "hi"       # one-shot, non-interactive
make verify                    # test + lint — run before every commit
```

You need a local OpenAI-compatible server (llama.cpp `llama-server`, LM Studio,
vLLM, …) or a remote endpoint via `--base-url` / `INVOKR_BASE_URL` +
`OPENAI_API_KEY`. Module is `github.com/antonikliment/invokr`, Go 1.26.

## The guides

| Guide | Covers | Packages |
|---|---|---|
| [01-big-picture](01-big-picture.md) | Architecture, one message's life | everything |
| [02-agent-loop](02-agent-loop.md) | Turn loop, prompt building, syntax, compaction | `agent/`, `capability/agentprompt/`, `capability/syntax/` |
| [03-provider](03-provider.md) | LLM client, streaming, error normalization | `clients/` |
| [04-tools](04-tools.md) | Tool registry, lifecycle, native tools, workspace/shell policy | `tools/`, `tools/core/`, `tools/system/` |
| [05-toolbox](05-toolbox.md) | Plugin tools, namespace sandbox, Python supply chain, docker sandbox | `tools/system/toolbox/`, `tools/system/create_tool/`, `capability/docker_sandbox/` |
| [06-memory](06-memory.md) | Memory tiers, curation, recall, forgetting, recall gate, transcripts | `capability/memory/`, `capability/sessionstore/` |
| [07-approvals](07-approvals.md) | Approval broker, batch queue, grants, per-frontend policies | `capability/approval/` |
| [08-events](08-events.md) | Event bus, audit events, the TUI threading rule | `platform/events/` |
| [09-tui](09-tui.md) | Bubble Tea UI, slash commands, plain mode | `cmd/agentui/` |
| [10-config](10-config.md) | Config layering, env overrides, validation | `config/` |
| [11-subagents](11-subagents.md) | delegate_task, agent definitions, tool filtering | `capability/agentdef/`, `tools/system/delegate/`, `platform/runtime/subagent.go` |
| [13-jobs](13-jobs.md) | Scheduled jobs, crontab, the schedule tool | `capability/jobs/`, `tools/system/schedule/` |
| [14-relay](14-relay.md) | agent-relay carries Telegram/Signal/Matrix to `invokr acp` | `surface/acpagent/` |
| [15-testing](15-testing.md) | Test style, the fake provider, e2e tiers, lint budgets | `support/testutil/`, `e2e/` |
| [16-diagrams](16-diagrams.md) | Mermaid diagrams: system context → layers → bootstrap → turn sequence → subsystem internals | everything |

## House conventions (the ones that bite)

- **Lint budgets are hard limits**: gocyclo ≤ 12, gocognit ≤ 15, funlen 80/50,
  nestif ≤ 5, dupl 100, and a repo-wide 30,000-LOC cap on implementation Go
  (`goclocbudget`). Large features can fail CI on *size alone*. Split
  functions early; delete code when you add code.
- **Errors**: wrap with `fmt.Errorf("ctx: %w", err)`; classify with typed
  errors via `errors.As` (`approval.DeniedError`, `core.ToolError`). Never
  string-match error messages (the one sanctioned exception is documented in
  `clients/openai.go`).
- **`panic` only for registration-time invariants** (e.g. a tool registered
  without a name), never for runtime errors.
- **Tests**: stdlib `testing` with direct `t.Fatalf` — testify is not house
  style. `_test.go` files are exempt from the complexity linters.
- **New packages need permission**: a new package requires that no owner in
  `docs/architecture/services.md` fits, that the doc is updated, and that the
  PR justifies it.
- **Don't trust model prose**: the runner's outcome ledger
  (`ToolFailures`/`ToolCallCount`) is the source of truth for what ran
  (ADR 0001). Tests assert on oracles, never on model text (ADR 0004).
- **TUI threading**: bus callbacks must go through the mutex-guarded
  `toolEventQueue`, drained inside the Bubble Tea loop. Never mutate UI state
  from a handler goroutine (see [08-events](08-events.md)).
- **`core.ToolError.Payload` semantics**: non-empty = completed-but-flagged
  (model still sees output, UI shows failure mark); empty = hard failure.
  Getting this wrong silently changes what the model sees.
- Follow `AGENTS.md` when working as an agent in this repo (tokf-wrapped
  shell commands, never push to `main`).

## Where things are decided

- Intentional trade-offs: `docs/adr/` and `docs/agents/adr/`.
- Package ownership: `docs/architecture/services.md`.
