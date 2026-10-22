# Invokr reference

Detailed reference for every feature, command, and subsystem. The quickstart lives in [README.md](../README.md); this is the long-form index.

# Reference

Config lives in `~/.invokr/config.json` (written by `setup`) and environment
overrides use the `INVOKR_` prefix — the on-disk names predate the rename and
are kept for compatibility.

## Commands

Command output is rendered for a human on a terminal and plain everywhere else
— see [docs/cli-output.md](docs/cli-output.md). Group commands act on a bare
invocation: `invokr sessions` is the history view,
`invokr jobs` lists jobs, `invokr config` prints the effective config.

```bash
invokr agent                 # interactive TUI (default)
invokr --resume              # pick a session from a menu, then resume it in the TUI
invokr --resume <id>         # resume a specific stored session in the TUI
invokr --last                # resume the most recent session in this directory
invokr exec "prompt"         # one-shot, non-interactive; --json emits a JSONL ledger
invokr exec --last "prompt"  # continue the most recent session (or --resume <id>)
invokr sessions              # history view; search/show/rm to drill in, --json to script it
invokr bot serve             # run Telegram/Signal chat transports
invokr jobs                  # scheduled-job store: add/list/rm/purge/run (add takes --allow <tier>)
invokr index                 # build the retrieval tool's workspace index; `index status` reports it
invokr doctor                # diagnose provider, model, sandbox, interpreters, MCP
invokr provider              # list/install verified model-provider binaries; set-key stores credentials
invokr config                # print effective config (secrets redacted); profiles/use switch profiles
invokr version               # same as --version
invokr setup                 # first-run configuration
```

## Approvals

`exec` (including `--resume`/`--last` runs) auto-denies tool approvals by
default. Opt in
with `--allow <tier>` (repeatable; `--allow read-only` covers the read-only
tiers) or `--allow-all` (approves everything — unattended arbitrary tool
execution). Neither combines with `--mode`. `jobs add` takes the same
`--allow <tier>`/`--allow-all` flags.

In the TUI, batched approvals collect in a queue rendered as
`approval queue (N pending)`. Keys: `a` approve / `d` deny the selected item,
`A` approve-all, `D` (or `esc`) deny-all, `v` expand details, `↑/↓` move the
cursor. Shift+Tab cycles Manual → Auto → Plan; `/mode manual|auto|plan`
selects one directly (`/approvals` is an alias). Auto approves workspace edits
and confined execution. Plan permits inspection and verification with read-only
sources. `/publishing ask|allow` controls recognized publishing in Auto.
`/reads workspace|ask` sets whether `read` and `file_search` may reach paths
outside the workspace for this session; with `ask` every such read prompts in
every mode, and `a` grants the containing directory for 24 hours. Persist it with
`workspace.reads` (`INVOKR_WORKSPACE_READS`), Ctrl+O → Setup, or `invokr setup`.
`--mode auto` and `--mode plan` also work with `exec`, which denies any remaining
interactive approval requests. Modes reset on launch, including resumed sessions.
In a non-git workspace, Auto removes a newly created root `.git` directory and
fails the call, so root `git init` cannot succeed. Initialize the repository in
Manual mode or your terminal before switching to Auto.
`--yolo` is CLI-only, bypasses Invokr approval/sandbox/workspace restrictions,
keeps explicit denials, and displays a permanent warning until restart.
Standing grants (`~/.invokr/grants.json`) make a narrow, expiring
auto-approval persistent — see the security model page.

## Configuration

Common environment overrides:

- `INVOKR_BASE_URL=http://localhost:8080`
- `INVOKR_MODEL=<model>` (unset: the first model the server lists)
- `INVOKR_PROVIDER_API_KEY=...` or `OPENAI_API_KEY=...`
- `INVOKR_MEMORY_DIR=/path/to/markdown-memory`
- `INVOKR_TOOLBOX_DIR=/path/to/toolbox`
- `INVOKR_TOOLBOX_ISOLATE_ENV=true`
- `INVOKR_TOOLBOX_ENV=KEY=value,OTHER=value`
- `INVOKR_GO_CACHE_DIR=/tmp/invokr-go-cache`
- `INVOKR_BROWSER_AGENT_STATE=/tmp/invokr-browser`
- `INVOKR_SHELL_ALLOWED_COMMANDS=ls,git,pwd,go,gofmt`
- `INVOKR_SHELL_DISABLED=true`
- `INVOKR_TOOL_RESULT_KEEP_TURNS=3`: tool results older than this many
  assistant turns collapse to a one-line stub, ahead of compaction; the newest
  error per tool and results the last reply referenced stay longer.
- `INVOKR_TOOL_RESULT_AGE_TURNS=8`: batches that collapse. Nothing is rewritten
  until an eligible result is older than this many turns, and then everything
  past the keep window collapses in one pass — rewriting history every turn
  would invalidate the prompt-prefix cache every turn. `0` ages on every pass;
  any other value must be at least the keep window.
- `INVOKR_CORE_TOOLS=schedule,delegate_task` (config `tools.core`): add
  optional tools to the protected core, so their full schemas ship from the
  first turn and the model calls them directly, with no `tools` enable/run
  step. They cannot be disabled in-session.
- `INVOKR_TOOLS=read_file,apply_patch,shell` (or `--tools`): advertise only
  these tools to the model. Everything is still registered and loaded; a
  narrower list just cuts tool-selection dilution, which small models are
  sensitive to. Unset advertises every tool, but only the core set (`shell`,
  `read_file`, `edit`, `file_search`, `git_read`, `tool_search`, `todo`)
  ships its full schema each turn; the rest is listed by name and loads on
  `tool_search` or its first call. A set allowlist ships every named schema.
- `INVOKR_TODO_DISABLED=true`: omit the `todo` tool and its planning rule.

Runtime config is centralized in `config.AppConfig`.

## Model selection

At startup Invokr checks the configured model against the provider's
`/v1/models` list and falls back to the first advertised model if it is missing
(logged as a warning). Inside the TUI, `/model` lists available models and
`/model <id>` switches for the session. Set the default with `INVOKR_MODEL` or
`provider.model` in config.

## Syntax highlighting

Fenced code in an answer is highlighted by chroma. The TUI's settings View tab
lists the 64 stylesheets it ships under "Syntax theme" and applies the choice
immediately; picking `auto` goes back to following the terminal background. The
choice is remembered. There is no `/syntax` slash command: the theme is a
permanent preference, so it is edited where the other permanent preferences are.

Set the default with `ui.syntax_theme` in config, `INVOKR_SYNTAX_THEME`, or the
optional question in `invokr setup`. Leave it empty for `auto`. A name chosen in
the TUI shadows the configured one, on the same principle as the other view
preferences.

## Shell tool

Shell command execution is a built-in native tool, separate from dynamic toolbox
tools:

```json
{
  "shell": {
    "enabled": true,
    "allowed_commands": ["ls", "pwd", "cat", "head", "tail", "wc", "find", "grep", "rg", "git", "go", "gofmt"],
    "env_passthrough": ["MY_EXTRA_VAR"]
  }
}
```

A usable command needs two halves: an `allowed_commands` entry (may run with
approval) and a tier rule in `tools/system/shell/rules.go` (may run without
one). A command with no rule prompts on every call — run `invokr doctor` to
spot that and to get a paste-ready `allowed_commands` line for your project
type (e.g. `npm` on a Node checkout).

Shell commands do not inherit the host environment: they see only `PATH`,
`HOME`, `LANG`, `TERM`, `TMPDIR` plus anything named in `env_passthrough`, so
host secrets (e.g. `OPENAI_API_KEY`) stay invisible. Set `"isolate_env": false`
(or `INVOKR_SHELL_ISOLATE_ENV=false`) to restore full inheritance.

## Dynamic toolbox tools

Toolbox tools are auto-discovered from `toolbox/*` using HAMR-style specs.
Use the native `shell` tool for simple Unix commands, and toolbox tools for
multi-step behavior that needs Python, Node, or Go.

- metadata: `tool.json` with `name`, `description`, `example`
- runtime entrypoint: `main.go`, `main.js`, or `main.py`
- request/response over stdin/stdout JSON

See `toolbox/README.md` for the contract and
[docs/security-model.md](docs/security-model.md) for the sandbox and
supply-chain controls.

## Custom subagents

The `delegate_task` tool runs bounded child agents in isolated sessions. A task
may target a custom agent defined in `~/.invokr/agents/<name>.md`:

```markdown
---
name: reviewer
description: Read-only reviewer.
model: qwen3-coder      # optional; inherits parent model if omitted
max_turns: 4            # optional
tools:                  # optional allowlist; omit for the full tool set
  - read_file
  - file_search
---
You are a code reviewer. Only read files; never modify them.
```

The frontmatter `name`/`description`/body are required. An explicit `tools:` list
filters the subagent's tool set; an unknown agent name is rejected with the list
of available definitions. Point elsewhere with `agents.dir` in config.

## Docker sandbox

Invokr can re-exec itself inside a container before it starts, isolating the
whole agent (distinct from the per-toolbox-tool sandbox).

```json
{ "sandbox": { "enabled": true, "engine": "docker", "image": "" } }
```

- `INVOKR_SANDBOX=true` enables it; `INVOKR_SANDBOX_ENGINE=podman` selects the
  container CLI; `INVOKR_SANDBOX_IMAGE=<tag>` pins a prebuilt image (skips the
  auto-build). `INVOKR_IN_SANDBOX` is set inside the container to stop recursion.
- Whole-agent Docker mode and the per-toolbox `unshare` namespace sandbox are
  **alternatives, not layers**: enabling Docker mode disables the per-toolbox sandbox
  inside the container (`INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1`), since nested `unshare`
  fails in unprivileged containers. See ADR 0005 for details.

## Chat transports (Telegram / Signal)

`invokr bot serve` exposes the agent over chat. It is single-instance per memory
dir — a second `bot serve` sharing the same `memory.dir` refuses to start
(exclusive lock on `<memory.dir>/bot-serve.lock`). Configure one or both transports:

```json
{
  "messaging": {
    "telegram": { "token": "...", "allowed_chat_ids": [123456789] },
    "signal":   { "socket": "/run/signal-cli/socket", "allowed_numbers": ["+15551234567"] }
  }
}
```

Allowlists gate who may talk to the agent and **fail closed**: an empty
allowlist admits nobody, and a transport refuses to start without one unless
`allow_all: true` is set explicitly. Signal talks to a `signal-cli` JSON-RPC
UNIX socket; Telegram long-polls `getUpdates`. Over chat, tool approvals are
answered with inline buttons (Telegram) or a `YES <id>`/`NO <id>` reply (Signal).

## Memory forgetting and the recall gate

Two opt-in memory extensions (both default-off, see `docs/memory.md`):

- **Forgetting** (`memory.forgetting.enabled`) — runtime memory records decay on
  an Ebbinghaus curve, are strengthened each time they are recalled, and are
  archived when retention drops below threshold. The `recall_archive` tool is
  the only way to search/restore archived records and always asks for approval.
- **Recall gate** (`memory.recall_gate.enabled`) — a cheap probe completion on
  the first message of a session decides whether long-term memory would help
  and, if so, injects a "Relevant Memory" block into the system prompt.

## Built-in memory models (Zerfoo)

Invokr embeds memories with an in-process Go model runtime
([github.com/zerfoo/zerfoo](https://github.com/zerfoo/zerfoo)) — **no
llama-server, no CGo, and no Python** — models are GGUF files loaded in-process.
**Summarization** runs on the main chat model (the provider you already
configure), not in-process, because Zerfoo's CPU decode is too slow. See
[docs/memory.md](docs/memory.md) for the full reference, model selection
rationale, and migration notes from the old `memory.llama.*` config.

Run setup and answer `yes` to "Enable built-in memory recall":

```bash
./invokr setup
```

Models are **not** downloaded at setup time; they download lazily from
HuggingFace on first use (cached under `~/.cache/zerfoo/models`). For offline
use, point at local GGUF files with the `*_path` fields/env vars.

Config:

```json
{
  "memory": {
    "zerfoo": {
      "enabled": true,
      "embedding_model": "Qwen/Qwen2.5-0.5B-Instruct-GGUF/Q8_0",
      "summarization_model": "Qwen/Qwen2.5-1.5B-Instruct-GGUF/Q4_K_M",
      "embedding_dimensions": 896
    }
  }
}
```

Environment overrides:

- `INVOKR_MEMORY_ZERFOO_ENABLED=true`
- `INVOKR_MEMORY_ZERFOO_EMBEDDING_MODEL=Qwen/Qwen2.5-0.5B-Instruct-GGUF/Q8_0`
- `INVOKR_MEMORY_ZERFOO_SUMMARIZATION_MODEL=Qwen/Qwen2.5-1.5B-Instruct-GGUF/Q4_K_M` (integration tests only; runtime summarizes via the chat model)
- `INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH=/path/to/embed.gguf` (offline override)
- `INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH=/path/to/sum.gguf` (offline override)
- `INVOKR_MEMORY_ZERFOO_EMBEDDING_DIMENSIONS=896`
- `INVOKR_MEMORY_ZERFOO_MAX_SUMMARY_TOKENS=512`
- `INVOKR_MEMORY_ZERFOO_MAX_RAW_EVENTS_BEFORE_SUMMARY=12`
- `INVOKR_MEMORY_ZERFOO_MIN_IMPORTANCE_TO_STORE=0.35`
- `INVOKR_MEMORY_ZERFOO_REQUEST_TIMEOUT_MS=120000`

When `memory.zerfoo.enabled=true`, Invokr uses vector recall automatically;
queries are embedded with query semantics and documents as passages, then ranked
by cosine similarity over Zerfoo's L2-normalized embeddings. When it is false (or
a model fails to load), recall falls back to SQLite FTS. Recall tier policy,
markdown memory filenames/limits, and result limits are internal constants.

Migrating from the old runtime: replace `memory.llama.*` config and
`INVOKR_MEMORY_LLAMA_*` env vars with the `memory.zerfoo.*` equivalents above.
There is nothing to install or build, and no `llama.cpp` checkout is needed.

## Tool result filtering (tokf)

Invokr can filter model-visible tool results through tokf when tokf is
installed and filtering is enabled. If tokf is enabled but unavailable, Invokr
logs a warning and continues with unfiltered tool results.

```bash
brew install mpecan/tokf/tokf
cargo install tokf
```

- `INVOKR_TOKF_DISABLED=true` disables tokf filtering.
- `INVOKR_TOKF_ENABLED=false` disables tokf filtering.
- `INVOKR_TOKF_BIN=/path/to/tokf` uses a specific tokf binary.
- `INVOKR_TOKF_HOME=/tmp/invokr-tokf` relocates tokf state/cache files.

## Logging

```bash
./invokr --log-level=debug                    # debug logging
./invokr --log-level=debug --log-format=json  # JSON logs
./invokr --log-level=info --log-output=invokr.log
```

`--log-filter` overrides the level for named components, in either syntax:

```bash
./invokr --log-level=warn --log-filter=agent.session=info,clients=debug
./invokr --log-level=warn --log-filter=info:agent.session,debug:clients
```

## Development

Full detail in [CONTRIBUTING.md](CONTRIBUTING.md).

```bash
make verify        # test + lint — run this before committing
make test          # go test ./...
make lint          # ./custom-golangci-lint if present, else golangci-lint run ./...
make lint-ci       # build the custom linter from .custom-gcl.yml, then run it
make analyze       # code-size/complexity report (writes ./size-report.html)
gofmt -w . && go mod tidy
```

CI is Woodpecker (`.woodpecker/{lint,test,e2e,pr-metrics}.yml`), gated on
PRs/pushes to `main`: `lint` runs the custom linter (`make lint-ci`), `test`
runs `make test`, `e2e` runs `make e2e`, and `pr-metrics` posts a code-size/
complexity diff as a PR comment. Lint enforces complexity budgets (gocyclo
≤12, gocognit ≤15, funlen 80/50, dupl) plus a hard implementation-LOC budget
(`goclocbudget`) — large features can fail CI on size alone. `_test.go` files
are exempt from the complexity linters.

The live e2e tier drives the real agent against a running model server:

```bash
./e2e-live.sh                       # whole live tier
./e2e-live.sh -run TestLiveD12 -v   # one case
```

The README demo GIF is reproducible with `make record` — see
[docs/assets/record/](docs/assets/record/README.md).

## File editing

The native `edit` tool accepts `path`, an `edits` list, and optional `dry_run`
and `delete` flags. Use the list even for one replacement:

```json
{"path":"config.go","edits":[{"old":"timeout = 10","new":"timeout = 30"}]}
```

Put several changes to the same file in one list. Each entry contains `old`,
`new`, and optional `replace_all`. Replacements apply in order against the
updated content; a failed entry prevents the entire call from being written.
Keep each `old` small but unique, and merge overlapping changes.

A single entry with empty `old` creates or overwrites the whole file, including
an empty file. `dry_run: true` validates without writing. To delete a file,
use `{"path":"config.go","delete":true}` without edits.

This replaces `edit_file`; explicit tool allowlists must use `edit`. Top-level
`old`, `new`, `replace_all`, and their former aliases are rejected. Existing
saved conversations can still contain the old calls, but new calls must use
the current name and argument shape.
