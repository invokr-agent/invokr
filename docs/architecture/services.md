# Invokr Service and Package Ownership

This document defines where responsibilities belong. New logic should extend these owners instead of duplicating them.

| Responsibility | Owner |
|---|---|
| CLI command registration and persistent flags | `cmd/` |
| Interactive terminal UI | `cmd/agentui/` |
| Runtime/service composition and cleanup | `platform/runtime/` |
| App configuration, defaults, file/env overrides | `config/` |
| Runtime turn/session protocol types (`Session`, `AgentResponse`, `ToolOutcome`, `TokenUsage`, ...) | `platform/turn/` |
| Model/provider client abstractions | `clients/` |
| Agent session orchestration and model/tool loop | `agent/` |
| Tool calling facade and tool metadata | `tools/` |
| Built-in system tools | `tools/system/` |
| Workspace path policy and shared workspace file-read primitives | `tools/system/workspace/` |
| Dynamic toolbox plugin loading, sandboxed execution, Python supply-chain checks | `tools/system/toolbox/` |
| Session tool availability and management | `tools.Caller`, `tools/system/tool_search/`; metadata in `capability/sessionstore/` |
| Agent-authored tool creation (writes+registers a new toolbox plugin) | `tools/system/create_tool/` |
| Tool argument/result schemas | `tools/core/` |
| Workspace doc indexing + search over the memory store (ADR 0015) | `tools/system/retrieval/` |
| Memory manager and recall orchestration | `capability/memory/` |
| Shared memory record/hit/prompt types and helpers | `capability/memory/core/` |
| SQLite memory storage (records, FTS5, vectors, doc-kind workspace mirrors) | `capability/memory/sqlite/` |
| Markdown memory documents | `capability/memory/markdown/` |
| Zerfoo memory model runtime | `capability/memory/zerfoo/` |
| Memory summary types/prompt | `capability/memory/summary/` |
| Prompt history | `capability/memory/history/` |
| Recall policy | `capability/memory/policy/` |
| Memory forgetting (decay + archive sweep) | `capability/memory/forgetting/` |
| First-turn recall gate (nothink probe) | `capability/memory/recallgate/` |
| Recall orchestration across pinned/lexical/vector sources | `capability/memory/recall/` |
| Language server process lifecycle and LSP conversation (spike) | `capability/lsp/` |
| Agent-facing code navigation tool (definition/references/symbols/diagnostics) | `tools/system/lsp/` |
| Skill definitions and manager | `capability/skills/` |
| Agent definition files and registry | `capability/agentdef/` |
| Prompt syntax helpers | `capability/syntax/` |
| Agent prompt construction | `capability/agentprompt/` |
| Event dispatch | `platform/events/` |
| `exec --json` NDJSON wire envelope (field keys + event-type constants shared by producer and e2e consumers) | `cmd/execwire/` |
| Tool approval workflow | `capability/approval/` |
| Scheduled job store, crontab boundary, and the job run loop (`jobs.Run`: approval drain, prompt send, status recording) | `capability/jobs/` |
| Bootstrap wrapper that supplies a fresh runtime session to `jobs.Run` (imports `platform/runtime`; kept out of `capability/jobs` to avoid an import cycle with `tools/system/schedule`) | `capability/jobs/jobrun/` |
| Agent-facing scheduled-job tool (list/add/modify/remove) | `tools/system/schedule/` |
| Agent-facing session todo list (state + tool) | `tools/system/todo/` |
| Logging buffer and logger setup | `platform/logging/` |
| Build-time version/commit/date (ldflags), and the non-blocking check for a newer release | `platform/version/` |
| First-run/setup flow | `platform/setup/` |
| Shared UI styles/helpers | `surface/ui/` |
| Test helpers | `support/testutil/` |
| Constants for built-in prompts/tools/policies | `constants/` |
| ACP agent over stdio (relays such as agent-relay drive it) | `surface/acpagent/` |
| Persisted session transcripts (JSONL) | `capability/sessionstore/` |
| Whole-agent docker/podman sandbox (image build + self-re-exec) | `capability/docker_sandbox/` |
| Tool-result filtering integration (tokf) | `tools/integrations/` |
| Atomic file-write + path-containment helpers (leaf package) | `support/fsutil/` |
| Numeric helpers: clamping + cosine similarity (leaf package) | `support/mathutil/` |
| String helpers: first-non-empty, UTF-8-safe truncation, SHA-256 hex, first-balanced-JSON-object extraction from model output (leaf package) | `support/strutil/` |
| Generic concurrency-safe name→definition registry (leaf package) | `support/registry/` |
| Markdown front-matter/body splitter (leaf package) | `support/frontmatter/` |
| Goose SQL migration runner behind a process-wide mutex (leaf package) | `support/migrate/` |
| E2E report generator | `cmd/e2ereport/` |

## Rule for new packages

A new package is allowed only when:

1. no existing owner fits,
2. the responsibility is added to this document,
3. the PR explains why extension was not enough.
