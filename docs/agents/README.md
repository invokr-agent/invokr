# Invokr Agent Docs

Agent-first context for Invokr. Read the smallest useful doc before scanning code.

## Read By Task

- Need package orientation: `project-map.md`
- Need startup/runtime/tool-turn flow: `runtime-flow.md`
- Touching agent, tools, memory, config, provider, skills, or UI: `subsystems.md`
- Doing architecture review or refactor planning: `architecture-risks.md`
- New to the codebase: `../onboarding/README.md`
- Wondering whether a tradeoff is intentional: `adr/`

## Current Shape

Invokr is a Go CLI agent with Cobra commands, a Bubble Tea interactive UI, plain
stdin mode, OpenAI-compatible provider transport, native and toolbox tools,
tagged-JSON syntax, filesystem skills, structured events, and
Markdown/SQLite/optional Zerfoo-vector memory.

Beyond the core loop it also carries several subsystems worth knowing before you
scan code:

- **Batch approval queue** (`capability/approval/`) — multiple concurrent tool
  approvals accumulate and are decided together (approve/deny per-item or all).
- **Custom subagents** (`capability/agentdef/`) — `~/.invokr/agents/<name>.md`
  definitions dispatched by the `delegate_task` tool, with per-agent prompt,
  model, turn budget, and tool allowlist.
- **Docker/podman sandbox** (`capability/docker_sandbox/`) — the agent can self-re-exec
  inside a container before bootstrap.
- **ACP agent** (`surface/acpagent/`) — the agent over Agent Client Protocol on stdio
  (`invokr acp`), driven by an editor or by agent-relay.
- **Scheduled jobs** (`capability/jobs/`, `capability/jobs/jobrun/`) — a crontab-backed
  job store with an agent-facing `schedule` tool.
- **Model selection** — startup availability check against `/v1/models` with a
  first-listed fallback, plus the in-TUI `/model` command.
- **Memory forgetting** (`capability/memory/forgetting/`) — opt-in Ebbinghaus decay
  with a cold archive and the approval-gated `recall_archive` tool.
- **Recall gate** (`capability/memory/recallgate/`) — opt-in first-turn probe that
  decides whether to inject a "Relevant Memory" prompt block.

Main composition happens in `platform/runtime/bootstrap.go`. Most packages are wired there and then injected through small interfaces or service structs.

## Ground Rules For Agents

- Use `TOKF_HOME=/tmp/invokr-tokf tokf run <command>` for shell commands.
- Prefer `rg` and focused file reads before broad scans.
- Treat `platform/runtime/bootstrap.go` as source of runtime truth.
- Treat `config.AppConfig.WithDefaults()` and env handling as source of config truth.
- Check ADRs before re-litigating tool visibility, runtime composition, memory tiering, or shell/workspace policy.
