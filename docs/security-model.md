# Security model

Invokr runs code an LLM asked for, on your machine, with your privileges. This
page is the single place that states what each execution path can do by
default, what gates it, and where the boundaries are *not*.

Read [SECURITY.md](../SECURITY.md) for the reporting process and the list of
documented non-boundaries. This page is the mechanics.

## The three things doing the gating

1. **Capability configuration** — what a tool is allowed to attempt at all
   (`shell.allowed_commands`, `toolbox.package_allow`, `mcp.servers.*.enabled`).
2. **Approval tiers** — every gated action carries a tier string; the tier
   decides whether you are asked. Interactive `agent` prompts; `exec` denies by
   default and approves only the tiers you name.
3. **Sandboxing** — namespace (Linux, per toolbox tool and per process-backend
   `code_interpreter` call), container (whole-agent or per `code_interpreter`
   call). Sandboxing is what still holds *after* you
   approve something you shouldn't have.

They are independent. Losing one does not disable the others, but only
sandboxing survives a bad approval.

## Execution boundaries at a glance

Approval authorizes an operation; it does not restrict the approved process.
These are source-level contracts, not a claim that containment has been tested
on the current host. Paths and overrides below link to the detailed policy.

| Execution path | Writable roots | Network | Credentials / environment | Process lifetime | Approval and unavailable-host behavior |
| --- | --- | --- | --- | --- | --- |
| [Shell with namespaces](#shell) | Workspace write roots, private temporary storage, selected build caches and linked Git dirs; read-only roots remain sealed | Host network for network tier or `allow_network_commands`; otherwise private network namespace | Filtered environment by default; `env_passthrough` adds named values; `isolate_env: false` inherits host env for ordinary commands; routed scripts always force the filtered env | Foreground calls use timeout/process-tree cancellation; explicit persistent work may outlive a call | Shell tiers still apply. Allowed ordinary commands fall back to host execution if namespaces are unavailable; interpreters/scripts refuse without explicit opt-out. Plan verification forbids host fallback. |
| [Shell host fallback / YOLO](#shell) | Host user permissions; path/tier checks are not OS confinement | Host network, even if policy rejects network commands | YOLO inherits full host env; ordinary fallback filters by configuration, and routed scripts force a filtered env | Same execution owner, without namespace containment | YOLO bypasses approvals and namespaces. Namespace failure is not permission to widen the allowlist. |
| [Toolbox namespace process or daemon](#toolbox-tools) | Declared workspace access (`WritesWorkspace` read-write, `ReadsWorkspace` read-only), sandbox temporary storage | Private unless `Network` declared; `allow_hosts` scopes DNS only | Filtered by default, plus `toolbox.env`; daemon environment is captured at startup | One-shot call has execution timeout; daemons persist across calls until eviction/shutdown. Cancellation closes daemon pipes and terminates its process group. | Manifest capabilities/tiers and approval policy apply. Unsupported hosts refuse unless `allow_unsandboxed` / `INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1`; opt-out runs with host privileges. |
| [MCP stdio](#mcp-servers) | Host user permissions; no workspace sandbox | Host network | Full inherited host environment, including secrets | Persistent subprocess, evicted on request failure or runtime cleanup; cancellation closes pipes and terminates the process group | Disabled until configured; tool calls ask by default. Server startup/discovery is not gated by per-call approval. No namespace isolation on any host. |
| [MCP HTTP](#mcp-servers) | Remote server's permissions, outside Invokr's control | Remote endpoint; HTTPS except loopback | Configured `headers_env` values; no full environment transfer | Persistent client/session; calls use context deadlines | Same per-server tool-call approvals. Remote server isolation/lifetime is unverified and operator-managed. Redirects are rejected; response messages are limited to 8 MiB. |
| [Whole-agent Docker/podman](#whole-agent-docker-sandbox) | Container filesystem plus read-write mounts of selected workspace and Invokr home | Engine default network; loopback provider URL can be mapped to host gateway | `INVOKR_*`, `OPENAI_API_KEY`, terminal variables; mounted config/memory may contain credentials | Attached container uses `--init` and `--rm`; outer process waits for container exit | Tool approvals remain active unless overridden. Missing engine/setup fails startup. Auto-building image requires Linux; other hosts need a supplied image and engine. Inner toolbox namespace isolation is disabled, so the outer container is the boundary. |

The implementation anchors are [shell execution](../tools/system/shell/exec.go),
[toolbox daemon lifecycle](../tools/system/toolbox/daemon.go),
[MCP transport](../tools/system/mcp/mcp.go), and
[container launch/environment](../capability/docker_sandbox/relaunch.go).
For container/process Python execution, see [`code_interpreter`](#code_interpreter).
Native file tools use workspace path policy rather than subprocess isolation.
A green portable test job with `INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1` does not
establish confinement; use the separate [containment gate](#mandatory-containment-checks)
on a supported host.

## Approval tiers

A tier is a plain string attached to an approval request. The ones in the tree:

| Tier | Emitted by | Meaning |
|---|---|---|
| `execute_readonly_command` | `shell` | Classified as read-only. **Never prompts.** |
| `execute_write_command` | `shell`, `tmux` | May modify files. Prompts unless `shell.allow_write_commands`. |
| `execute_network_command` | `shell`, `code_interpreter` | Can reach the network. Prompts unless `shell.allow_network_commands`. |
| `write_workspace` | `apply_patch`, `edit`, `git_write`, `create_tool` | Writes workspace files. |
| `read_workspace` | `file_search`, `tool_search`, `retrieval`, `code_interpreter` | Reads workspace files. |
| `retrieval_index_write` | `retrieval` | Writes the workspace index. |
| `code_execution` | `code_interpreter` | Runs Python. |
| `artifact_write` | `code_interpreter` | Writes session artifacts. |
| `web_network` | `web_search`, `web_fetch` | Outbound search queries and URL fetches. |
| `delegate_task` | `delegate_task` | Spawns a subagent. |
| `write` | `schedule` | Creates or changes scheduled jobs. |
| `mcp_tool` | every MCP tool | Calls a third-party MCP server. |
| `dangerous` | toolbox dependency install | Installs resolved Python packages. |

Toolbox tools and `create_tool` also emit whatever tiers the manifest declares,
comma-joined.

Tools also declare an approval *policy* — `auto`, `ask`, or `deny`
(`constants/approval.go`) — which is what decides whether a request is raised
before the tier is ever consulted.

In Auto mode, `ask` still auto-approves a sandbox-confined call. A toolbox
manifest that must not run unattended uses `ask_once` (prompt until a standing
grant covers the call; blocked in Plan) or `always_ask` (prompt every call; grants ignored).

### In the TUI

Pending approvals collect in a boxed queue (`APPROVAL  X of N`):
`y` yes, `a` always (grants the first two words of the command, e.g. `go test *`; shown only when a safe pattern exists), `n` no, `Y` yes-to-all, `N`/`esc` no-to-all, `v` details.
Shift+Tab cycles Manual, Auto, and Plan; `/mode` selects one directly and
`/approvals` is an alias. Auto approves workspace edits and execution whose
owner proves confinement, including network and local commits. Protected edits,
unconfined execution, and recognized publishing still ask. `/publishing allow`
authorizes publishing for this session. Plan denies source writes and publishing;
verification uses read-only sources and separate scratch/cache, and network asks.
Modes never persist. See [ADR 0007](agents/adr/0007-session-approval-modes.md) for
execution boundaries, protected mount limitations, and the publishing guard's scope.

`--yolo` bypasses Invokr approval and sandbox/workspace restrictions for the
session. It is CLI-only, shows a permanent top warning, and cannot be entered
or exited through the TUI. Explicit standing denials still apply.

### In `exec`

`exec` is non-interactive, so Manual **auto-denies every approval by default**.
`--mode auto|plan` applies the same session policy as the TUI and denies requests
that still need interactive approval. Auto publishing needs `--allow-publishing`.
Mode flags cannot be combined with the following legacy allow flags.
Opt in per tier:

```bash
invokr exec "…" --allow read-only          # execute_readonly_command + read_workspace
invokr exec "…" --allow execute_write_command
invokr exec "…" --allow write_workspace    # apply_patch, write_file
invokr exec "…" --allow-all                # see below
```

A request whose tier is empty is never auto-approved, and a comma-joined tier
list is approved only if *every* component tier is allowed.

`apply_patch` raises an approval on every real apply (only a clean dry run is
auto-approved), so a non-interactive run that edits files needs
`--allow write_workspace`; without it every patch is denied. The same applies to
scheduled jobs, which auto-deny unless added with `jobs add --allow <tier>`
(same tier matcher as `exec`) or `--allow-all`.

### What `--allow-all` gives away

`--allow-all` approves every request from every tool, unattended, for the whole
run: arbitrary shell in the write and network tiers, arbitrary Python, every
MCP tool on every configured server, and dependency installation. It is
equivalent to handing the model your shell. Invokr prints a warning to stderr
when you use it. Prefer naming tiers, or a standing grant.

### Standing grants

A standing grant (`capability/approval/standing.go`, `~/.invokr/grants.json`,
`0600`) is a persistent, narrow, expiring auto-approval — "always allow `shell`
running `git status*` for 24h". It exists so prompt fatigue does not push people
to `--allow-all`, which is strictly worse.

- Matching is deny-by-default and literal: exact tool name plus an explicit
  glob. No regex, no substring matching. Patterns are rejected if they start
  with `*`, contain `**`, `..` or shell metacharacters, are not printable
  ASCII, or have fewer than 4 literal characters.
- Scopes are `session` (memory only, never written to disk), `workspace`, and
  `global`.
- `create_tool`, `delegate_task` and `code_interpreter` can never be *granted* — they author or
  delegate new capability rather than performing one bounded action. They can
  still be denied: auto-denying is always safe.
- Creation and every use emit an audit event. A rule that decides silently is
  not auditable.

### Standing denials

A standing denial is the same record with `deny` set: same file, scopes,
expiry, pattern grammar, and audit trail. It is created with
`/grants deny <tool> <hours> <pattern>`, and
listed and revoked through `/grants`.

- **Precedence is deny-overrides.** If both a grant and a denial apply to a
  call, the call is denied — regardless of which was created first, and
  regardless of read-only auto-approval or an `--allow`ed tier.
- A matching denial short-circuits the approval queue: the call fails with
  `approval.DeniedError` (classified with `errors.As`, never string-matched),
  the model sees the flagged tool error, and no prompt is shown.
- Denials persist, so they still apply after `--resume` and in `exec` and plain
  mode. `exec` without a matching rule keeps its default deny-all behavior.
- Revoking a denial restores normal ask-mode prompting for that pattern.
- **A path-list subject is matched per path, with the quantifier set by the
  rule's direction.** `apply_patch` presents every path it touches as one
  space-separated subject. A grant applies only if it covers *every* path; a
  denial applies if it matches *any* path. Requiring every path for a denial
  would let a patch escape it by adding one unrelated file — and land on a
  broader grant instead.
- `~/.invokr/grants.json` is a versioned JSON object (`{"version":2,"rules":
  […]}`), not the bare array written before denials existed. The array form is
  refused outright: `deny` is `omitempty`, so a binary predating denials would
  otherwise read a stored denial as a plain grant and auto-approve what the user
  chose to always deny. Refusing the file means a downgrade prompts (fail
  closed) instead of allowing. A file that will not parse drops *all* rules,
  denials included, and is logged at `ERROR`.

Known residual: pattern matching is leftmost-first without backtracking, so an
ambiguous pattern fails to match rather than matching. For a grant that fails
closed; for a denial the same conservatism means the denial may not fire, and a
broader grant can then approve the call. Prefer denial patterns with a single
wildcard.

## `shell`

The `shell` tool runs each line with `bash -c`, but only after the line is
parsed and walked by an AST policy (`tools/system/shell/policy.go`) that checks
every command against the allowlist and assigns the tier. `NAME=` prefixes that
set loader variables are refused. When the namespace sandbox is available the
line runs inside it (ADR 0016).

The allowlist is not the security boundary — the tier classifier is.
`shell.allowed_commands` decides what may run **with approval**;
the rule table in `tools/system/shell/rules.go` decides what runs **with no
human in the loop**. A usable command needs both halves; `invokr doctor`
warns when a configured command has no rule (it would prompt on every call)
and suggests the allowlist line for the detected project type. Detection only
ever advises — workspace content can never widen what the agent executes
(ADR 0011, whose shell section ADR 0016 supersedes).

Defaults:

- **Allowlist** (`constants/shell.go`): inspection tools (`ls`, `cat`,
  `grep`, `rg`, `find`, `sed`, `awk`, `diff`, …), `git`, `go`, `gofmt`, the
  wrappers `xargs` and `timeout` (classified by the command they run), and
  workspace-shaping writes (`chmod`, `mkdir`, `cp`, `rm`, `rmdir`, `mv`,
  `touch`), which are write tier and ask. Ecosystem drivers (`make`, `npm`, `cargo`, `node`,
  `python3`, `pytest`) must be opted into `shell.allowed_commands`; `npm` and
  `cargo` already have tier rules, the rest stay fail-closed write. Anything
  not allowlisted is refused before it runs, except interpreters and
  workspace scripts (`python`, `node`, `bash`, `./run.sh`, …), which run only
  inside the sandbox and are refused without one unless
  `INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1`. A configured
  `shell.allowed_commands` replaces the default list, but a read-only
  inspection baseline (`ls`, `pwd`, `cat`, `head`, `tail`, `wc`, `find`,
  `grep`, `rg`, `echo`, `which`, `file`, `sort`, `uniq`, `diff`, `stat`,
  `date`, `xargs`) is always unioned in: none of them writes or executes project code,
  so a config written before the default list grew cannot narrow the agent
  below "can look at the workspace".
- **Tier classification** is declarative, one `commandRules` row per
  executable (`tools/system/shell/rules.go`), and can only narrow the
  fail-closed default: no rule means write tier, and within a rule anything
  unnamed stays write. `git status|diff|show|log|rev-parse|branch --list|stash list`
  are read-only; `git fetch|pull|clone` are network; `go version|list|vet|env`
  (without `-w`/`-u`) and `gofmt` without `-w` are read-only. Flag-only
  utilities stay read-only unless a flag or argument breaks the claim: write
  flags (`git log --output=…`), unlisted flags on allowlist-carrying rows
  (`find -delete`, `rg --pre`), and paths that leave the workspace
  (`cat /etc/passwd`, `wc --files0-from=/etc/passwd`) all demote to write.
  `curl`/`wget` are network tier whatever their argv: they prompt unless
  `allow_network_commands`, which is also what keeps the host network
  namespace for the line (#545).
- **Environment is isolated by default.** Commands see only `PATH`, `HOME`,
  `LANG`, `TERM`, `TMPDIR`, the Go/Java cache and home variables (`GOPATH`,
  `GOMODCACHE`, `GOCACHE`, `JAVA_HOME`, `GRADLE_USER_HOME`) plus `shell.env_passthrough`, so host secrets
  (`OPENAI_API_KEY` and friends) are invisible. `shell.isolate_env: false`
  restores full inheritance.
- **Limits:** 30s timeout and 20KB of output by default, capped at 120s /
  200KB even if the model asks for more.

`shell.allow_write_commands` / `allow_network_commands` turn whole tiers into
silent auto-approvals. `shell.enabled: false` (or `INVOKR_SHELL_DISABLED=true`)
removes the tool entirely.

**Where the sandbox is available, it is the boundary for `shell`**: lines
outside `--yolo` run in a user+mount+PID namespace. Configured workspace roots,
per-workspace `/tmp`, selected build caches, and linked Git directories can be
writable. Network-tier lines and lines run with `allow_network_commands` retain
the host network; other lines have no network. **Without the sandbox, or under
`--yolo`, allowed commands run unconfined on the host**. Routed interpreters and
scripts are refused on an unsupported host unless the operator opts out with
`INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1`.

## Toolbox tools

Toolbox tools are agent- or user-authored scripts (Python/Node/Go) discovered
from `toolbox/*`. They are the one path where sandboxing is mandatory rather
than optional.

**Linux namespace sandbox.** Each invocation runs under `unshare` with a
rootless user namespace mapped uid→0, plus private mount, PID, UTS and IPC
namespaces. The root filesystem is built from scratch by read-only bind-mounting
only the paths the tool needs at their original absolute paths, then
`pivot_root . .` with a lazy unmount of the old layer — the idiom that leaves no
reachable path back to the host root.

Mounts follow the tool's declared capabilities:

- Always read-only: `/usr`, `/bin`, `/lib`, `/lib64`, the tool's own directory,
  the interpreter's directory, and the Python venv.
- Workspace: read-write if the tool declares `WritesWorkspace`, read-only if it
  declares `ReadsWorkspace`, absent otherwise.
- **Network namespace is unshared unless the tool declares `Network`** — a tool
  without that capability has no network at all, not a filtered one. Declaring
  it also mounts `/etc/resolv.conf`, `/etc/hosts`, `/etc/nsswitch.conf`,
  `/etc/services` read-only. A manifest may scope network access to named hosts
  (`network: { allow_hosts: [...] }`), which replaces those mounts with a
  generated hosts file resolving only the allowed names — that is DNS-level
  scoping, not egress control; see the non-boundary list below. Whenever the tool has
  network, the host's CA trust store (`/etc/ssl`,
  `/etc/ca-certificates`, `/etc/pki`) is also bound read-only so TLS
  verification works inside the sandbox.
- `toolbox.memory_limit_bytes` bounds the process via `RLIMIT_AS`;
  `toolbox.exec_timeout_ms` is the wall-clock backstop against runaway
  processes that resource limits alone don't contain.

**Off Linux there is no namespace sandbox** (`sandbox.Available()` in `tools/core/sandbox`
returns false on every non-Linux build). Toolbox execution is then **refused** rather than
silently running unconfined. `toolbox.allow_unsandboxed: true` /
`INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1` opts out; that means tools run on the host
with no isolation, and it is a documented non-boundary, not a bug.

**Environment isolation** works like `shell`: `toolbox.isolate_env` defaults to
true, with extras named in `toolbox.env`.

### Supply-chain controls on dependencies

Installing Python requirements for a toolbox tool is gated end to end
(`tools/system/toolbox/supplychain.go`):

1. pip resolves **through a local proxy** that only permits the configured
   `package_index_hosts` and `package_artifact_hosts`.
2. The resolution report is validated against `package_deny` (checked first,
   always blocks) and `package_allow` (if non-empty, an exact allowlist), plus
   `release_cooldown_days`, which rejects releases newer than the cooldown —
   the window in which a compromised release is usually caught.
3. You approve the fully resolved set — names, versions, URLs, SHA256s, release
   age — at tier `dangerous`. There is no approval of an unresolved
   `requirements.txt`.
4. Approved packages are written to a `requirements.lock` and installed from
   verified downloads.

Resolution includes already-installed packages, so the report covers the full
dependency graph. Names use Python normalization: case is ignored and runs of
`-`, `_`, or `.` are equivalent. Cached per-tool environments retain the validated
graph in their installation marker and recheck it against the current policy on
every use; editing the generated lock does not change that recorded graph.
Legacy or malformed markers require fresh resolution. A missing graph blocks reuse.
Package restrictions require per-tool environments: unset `INVOKR_TOOLBOX_VENV_DIR`
when using `INVOKR_TOOLBOX_PKG_ALLOW` or `INVOKR_TOOLBOX_PKG_DENY`. Shared environments
can change another tool's dependencies and cannot prove the recorded graph is current.

### Mandatory containment checks

`make containment` runs selected filesystem, network, environment, cancellation,
parent-death and protected-path tests with the race detector. Every required test
must emit a pass event; skipped tests/subtests and missing results fail the gate.
The portable test lane retains its existing fallback behavior.

`.woodpecker/containment.yml` needs a namespace-capable Linux host and a trusted
private repository permitting the dedicated privileged test step. Privilege belongs
to the outer test container; Invokr's children still exercise their own namespace
boundary. See [Woodpecker's privileged-step requirements](https://woodpecker-ci.org/docs/usage/workflow-syntax#privileged-mode).
Require `ci/woodpecker/pr/containment` for merging when repository branch protection
is available. As of this change, GitHub rejects branch-protection access for this
private repository with HTTP 403 (plan upgrade required); enforcement is pending,
not implied by the workflow file.

## `code_interpreter`

Backend selection: if `code_interpreter.backend` is unset, Invokr **prefers a
container engine** — `docker` then `podman`, whichever is on `PATH` — and falls
back to `process` only if neither is.

**Container backend.** `run --rm` with `--network=none` unless
`container_network` is set, inputs mounted `:ro`, artifacts `:rw`, `main.py`
`:ro`, a clean `PYTHONPATH`/`PYTHONNOUSERSITE`, and optional `--memory` /
`--cpus`. Default image `python:3.12-slim`, 512MB, 30s (capped 120s).
Enabling `container_network` adds the `execute_network_command` tier and always
prompts.

**Process backend** runs Python in the same unprivileged namespace sandbox as
toolbox tools when it is available: workspace roots read-only, only the
artifacts directory writable, a private `/tmp`, and no network unless
`container_network` is set. Without the sandbox (every non-Linux build, or a
host that cannot create user namespaces) the call is **refused** unless
`INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1` is set; Python then runs directly on the
host with your privileges and is **not a security boundary**. `--yolo` skips
the sandbox and always runs Python on the host.

The pre-flight token scan (`import subprocess`, `os.system`, `os.environ` when
`allow_env` is false, …) is **advisory only** — it annotates the approval
prompt and is trivially bypassable by any code that wants to. Do not treat it
as enforcement.

## MCP servers

**MCP servers are unsandboxed.** A stdio server is a subprocess spawned with
`exec.Command(server.Command, server.Args...)` and inherits the host
environment, including your secrets. An HTTP server is a remote third party.
Neither runs under any Invokr sandbox. Configuring an MCP server is a decision
to trust that code the same way you trust anything you run yourself.

Consequently:

- Servers are **off unless `enabled: true`**.
- The default tool approval mode is `ask` for **every** transport, and the
  default deliberately does not relax for HTTP: an MCP server's tool
  descriptions are model-visible untrusted text delivered by a third party.
  `default_tools_approval_mode: "auto"` disables that prompt — per server, and
  it is your call.
- `enabled_tools` / `disabled_tools` narrow the surface per server. Discovery
  follows `tools/list` cursors before applying these filters. A repeated cursor,
  more than 32 pages or 4,096 tools, a response over 8 MiB, or a request failure
  aborts discovery without publishing a partial tool list. The 30-second
  discovery context covers the whole page sequence.
- HTTP credentials are named by `headers_env` (header name → env var name) and
  read from the environment at connect time. They are never stored in the
  config file in plaintext.
- Every MCP tool is registered with `Network` and `ExecutesCode` capabilities,
  so it is excluded from anything keyed on those.

## Provider processes

Providers other than `openai-compatible` run as separate
`go-inference-router-provider-<type>` binaries that the router launches
(`clients/router.go`). These are not sandboxed: a provider process receives
your API key and the full conversation, and runs with your privileges.
Two controls bound what gets launched:

- **Signed installs.** `invokr provider install` verifies each binary against
  an Ed25519-signed release registry (`clients/provider_install.go`): the
  download is bounded by the size the manifest names, hashed as it streams,
  and the digest is checked again before the binary is ever launched. The
  plan — size and digest — is shown before any bytes are transferred.
- **Resolution order.** The verified install cache is consulted first, so a
  reinstalled provider always wins. **PATH lookup remains enabled as a
  documented migration concession**: an unsigned binary named
  `go-inference-router-provider-*` on PATH still runs if no verified install
  exists for that type. This is deliberate — it keeps providers installed via
  the previous `go install` path working for one release — and removing it is
  the stated end state. Until then, treat writable PATH directories as part of
  the trust boundary, or pin `router.providers[].path` to an explicit binary.

## Whole-agent Docker sandbox

`sandbox.enabled` makes Invokr re-exec itself inside a container before
bootstrap, isolating the entire agent rather than individual tools.
`INVOKR_IN_SANDBOX` is set inside to stop recursion; `sandbox.engine` selects
`docker` or `podman`; `sandbox.image` pins a prebuilt image and skips the
auto-build (auto-build is Linux-only).

Docker mode and the per-toolbox namespace sandbox are **alternatives, not
layers**: inside the container the per-toolbox sandbox is disabled, because
nested `unshare` fails in an unprivileged container. See ADR 0005.

## Other surfaces worth knowing about

- **Chat transports** (`bot serve`) fail closed. An empty allowlist admits
  nobody, and a transport refuses to start without one unless `allow_all: true`
  is set explicitly. Approvals over chat are inline buttons (Telegram) or
  `YES <id>` / `NO <id>` replies (Signal).
- **`web_search`** auto-approves ordinary queries but **prompts** when the query
  looks sensitive: an email address, a token-shaped string, a filesystem path, a
  secret-shaped literal, or any configured `sensitive_terms` / `private_hosts` /
  `private_repo_names`. `web_search.allow_sensitive` turns the prompt off. The
  point is to keep your private context out of a third-party query string.
- **`web_fetch`** shares the `web_network` tier and the same sensitive-target
  prompt. Private, loopback, and link-local addresses are refused at dial time
  (after DNS resolution, so rebinding does not help), which is the SSRF
  boundary. `web_fetch.allow_private_hosts` disables that guard **entirely** —
  it is an opt-out of the boundary, not a tuning knob.
- **Workspace confinement**: `workspace.root` plus `extra_roots` bound where
  file tools may reach; `workspace.readonly_roots`
  (`INVOKR_WORKSPACE_READONLY_ROOTS`) adds roots that are readable but refuse
  every write — `edit`, `apply_patch`, `code_interpreter` outputs and the
  shell's writable mounts all deny them; hidden paths are excluded unless
  `workspace.allow_hidden_paths` is set. The spill directory (below) is
  auto-appended to the extra roots so `read_file` can page spilled output;
  the policy makes no read/write distinction, so write tools can reach it too.
- **State on disk**: `~/.invokr` holds config, memory, sessions, and
  `grants.json` (`0600`). Session transcripts contain everything the model saw.
  `~/.invokr/spill` holds oversized tool output verbatim (`0600`, pruned after
  24h) — full command output lands there even when the model only saw a stub.
- **Secrets in config**: `config.json` is written `0600`, but
  `provider.api_key`, `router.providers[].api_key`, and
  `messaging.telegram.token` store credentials in plaintext when set. Every one
  of them has an environment alternative — `router.providers[].api_key_env`,
  `INVOKR_TELEGRAM_TOKEN`, and the provider env vars — and MCP HTTP headers can
  *only* be supplied via `headers_env`. Prefer the env forms; `invokr config`
  output masks keys either way (`config/redact.go`), but the file itself does
  not.

## What is explicitly not a boundary

Repeated from [SECURITY.md](../SECURITY.md) because it belongs here too:

- The `code_interpreter` process backend when unsandboxed (opt-in or `--yolo`),
  and its advisory token scan at all times.
- Toolbox execution under `INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1`.
- `shell` on a host without the namespace sandbox, or under `--yolo`.
- Toolbox scoped network (`allow_hosts`): it only controls name resolution via
  a generated hosts file. A tool that dials an IP literal, or ships its own
  resolver, reaches anything the namespace can. Containment is the unshared
  namespace (no `Network` capability); `allow_hosts` is a convenience filter.
- MCP servers of any transport.
- Approval gating itself: it is user *consent*, not containment. Once approved,
  an action runs with your privileges.
- The model. Prompt injection that makes the model *request* something dangerous
  is expected; the defence is the gate and the sandbox. A request that
  *bypasses* the gate, or escapes a sandbox that was supposed to hold, is a
  vulnerability — report it.

Run `invokr doctor` to see which of these are actually active on your machine:
it reports namespace sandbox availability, container engine presence, the
configured `code_interpreter` backend (not the auto-selected one), and the
grants file state.
