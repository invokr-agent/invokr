# Invokr Project Map

## Entrypoints

- `main.go`: setup preflight through `platform/setup`, default config load, Cobra execution.
- `cmd/root.go`: global flags, runtime bootstrap lifecycle, cleanup.
- `platform/runtime/bootstrap.go`: composition root for provider, events, approvals, memory, skills, tools, syntax, session, prompt history, and (when enabled) docker sandbox re-exec.
- `agent/runner.go`: bounded turn loop.
- `capability/syntax/tagged_json.go`: tagged-JSON tool-call and reply protocol.

### Commands (`cmd/`)

- `agent` (`agent_cli.go`): interactive TUI / plain-mode session.
- `exec` / `resume` (`exec_cli.go`): non-interactive one-shot and session-resume runs, JSONL ledger output.
- `bot serve` (`chat_cli.go`): Telegram/Signal chat transport loop.
- `sessions` (`sessions_cli.go`): list/search/show stored sessions.
- `jobs` (`jobs_cli.go`): scheduled-job store CRUD + `run`.
- `index` (`index_cli.go`): sync workspace docs into the memory store as `doc`-kind
  `memory_records` for the retrieval tool (ADR 0015).
- `setup` (`setup_cli.go`): first-run flow.
- `config show` (`config_cli.go`): print effective config with secrets redacted.
- `tokf-filter-stdin` (`tokf_filter_stdin.go`): hidden helper invoked by tokf result filtering.

## Package Map

- `agent/`: session facade, runner, model gateway, turn processor, native tool codec.
- `clients/`: OpenAI-compatible provider client, metadata, streaming, embeddings.
- `cmd/`: Cobra commands, setup command, TUI/plain execution.
- `config/`: app config, defaults, env overrides, config file load, prompt history/session interfaces.
- `constants/`: defaults and stable names for tools, memory, provider, shell, skills, prompts.
- `capability/agentprompt/`: prompt builder. The system prompt is the
  cacheable prefix (base prompt, environment, skills, tool specs, in that
  fixed order); recalled memory rides the user message so a provider prompt
  cache keeps hitting across turns.
- `capability/agentdef/`: `~/.invokr/agents/*.md` custom subagent definitions + registry.
- `capability/approval/`: tool approval broker and the batch approval queue.
- `platform/events/`: event model and the ordered dispatcher.
- `platform/turn/`: runtime turn/session protocol types (`Session`, `AgentResponse`, `TokenUsage`, tool-outcome guard).
- `capability/memory/`: manager + curator, Markdown pinned memory, SQLite records/FTS/vectors, Zerfoo embedding runtime, chat-model summarization, prompt history, recall policy/orchestrator, recall gate (`recallgate/`), forgetting decay (`forgetting/`), summary types.
- `surface/messaging/`: Telegram/Signal transports, router/serve loop, chat→session store.
- `capability/jobs/`, `capability/jobs/jobrun/`: scheduled-job store + crontab boundary, and its runtime executor.
- `capability/docker_sandbox/`: docker/podman image build and self-re-exec relaunch.
- `capability/sessionstore/`: persisted session transcripts.
- `platform/runtime/`: service bundle construction and cleanup (composition root).
- `platform/setup/`: first-run setup (no llama.cpp; Zerfoo models download lazily).
- `capability/skills/`: filesystem skill discovery and cache.
- `capability/syntax/`: prompt syntax strategy (tagged-JSON) and native tool codec.
- `tools/`: central caller, tool specs, and result filtering.
- `tools/integrations/`: tokf tool-result filter.
- `support/fsutil/`, `support/testutil/`: atomic-write helper, shared test helpers (incl. the `providertest` fake provider).
- `tools/system/`: native tools — `tools` manager (in `tool_search/`), `apply_patch`, `file_search`,
  `web_search`, `web_fetch`, `session_search`, `retrieval`, `skill`, `memory`, `shell`,
  `code_interpreter`, `tmux`, `delegate` (subagents), `schedule` (jobs), `mcp`,
  `toolbox` discovery, internal `create_tool` (via `tools.create`), and the `workspace` policy.
- `toolbox/`: dynamic plugin examples and built-in plugin specs/runtimes (incl. the browser agent daemon).

## Tests And Checks

- Standard check: `go test ./...`
- CI is Woodpecker (`.woodpecker/{lint,test,e2e,pr-metrics}.yml`): `make lint-ci`, `make test`, `make e2e`, and a PR code-metrics comment.
- High-value test areas: CLI/TUI, syntax parsing, caller/toolbox loading, shell policy, web search, setup, memory manager/recall/history.

## Source Anchors

Start from these before broad scans:

- Runtime wiring: `platform/runtime/bootstrap.go`
- Config shape: `config/config.go`
- Tool registry: `tools/caller.go`, `tools/system/load_system_tools.go`
- Toolbox runtime: `tools/system/toolbox/toolbox.go`
- Workspace/shell policy: `tools/system/workspace/workspace.go`, `tools/system/shell/shell.go`
- Memory: `capability/memory/manager.go`, `capability/memory/recall/orchestrator.go`, `capability/memory/sqlite/init_db.go`
- Prompt: `capability/agentprompt/builder.go`, `capability/syntax/tagged_json.go`
- UI: `cmd/agentui/model.go`, `cmd/agentui/run.go`
- Approvals: `capability/approval/approval.go`, `capability/approval/queue.go`
- Memory extensions: `capability/memory/recallgate/gate.go`, `capability/memory/forgetting/retention.go`, `capability/memory/curator.go`
- Subagents: `capability/agentdef/agentdef.go`, `tools/system/delegate/delegate.go`, `platform/runtime/subagent.go`
- Sandbox: `capability/docker_sandbox/relaunch.go`, `capability/docker_sandbox/image.go`
- Messaging: `surface/messaging/router.go`, `surface/messaging/telegram/telegram.go`, `surface/messaging/signal/signal.go`
- Jobs: `capability/jobs/`, `capability/jobs/jobrun/`, `tools/system/schedule/`
