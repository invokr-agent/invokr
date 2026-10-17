# 04 — Tools

Packages: `tools/` (the `Caller` registry), `tools/core/` (types + generic
lifecycle), `tools/system/*` (one subpackage per native tool),
`tools/integrations/` (tokf result filter).

## The registry: `tools.Caller`

One struct owns every tool: `specs` (ordered), `native` (name → handler),
`loadedSchemas` (per-session deferred-tool state), a `revision` counter, and
the result filter. It implements `core.ToolCaller`, which is what sessions and
subagents consume — `filteredCaller` (subagent allowlists) wraps the same
interface.

**Registration paths:**

- `RegisterNative(spec, handler)` — built-ins, at bootstrap via
  `tools/system/load_system_tools.go`.
- `ReplaceNativeToolsBySource(source, tools)` — dynamic sets (toolbox plugins
  under source `"toolbox"`, MCP servers under `"mcp:<name>"`). Replaces the
  whole source atomically; names colliding with a different source are
  skipped and reported.

**Deferred visibility.** Tools with `Metadata.AlwaysVisible=false` appear in
prompts as name+description only (no schema) until the model calls
`tool_search` with `include_schema=true`, which calls `MarkSchemasLoaded` for
the session. `HandleCall` rejects calls to unloaded tools with a hint to use
`tool_search`. Session state is dropped by `ForgetSession` when the session
ends (wired through `agent.Session.Close`). This keeps
big tool sets (MCP, toolbox) from bloating every prompt.

**HandleCall pipeline:** panic recovery → deferred-schema gate → native
handler → tokf result filter. A `core.ToolError` with a non-empty
`Payload` is "completed but flagged": the payload (filtered) still goes to the
model; the UI shows the `Code` as a failure marker.

## Writing a tool: `core.Tool[A]`

```go
core.Register(c, core.Tool[myArgs]{
    Spec:     core.ToolSpec{Name: "...", Description: "...", Example: "...",
              Parameters: core.SchemaFor[myArgs](), Metadata: core.ReadWorkspaceMetadata(...)},
    Decode:   optional,          // default: json.Unmarshal of call.Arguments
    Prepare:  optional,          // validate/normalize args
    Approve:  optional,          // return core.Auto() / core.Ask(req) / core.Deny(reason)
    Approver: deps.Approver,
    Execute:  required,          // the work; return payload or error
    AuditFailure: optional,      // fires for decode/prepare/approve failures only
})
```

`core.SchemaFor[A]()` derives the model-facing JSON Schema from the args
struct's tags: `type` from the Go type, `required` from the absence of
`omitempty`, and `description`/`enum` from `desc:` and `enum:` tags. Values only
known at runtime go through `core.WithEnum("field", values)`; a genuinely
bespoke schema can still be written as a literal.

The lifecycle is `Decode → Prepare → Approve → Execute`. `Register` panics on
a missing name or Execute — registration-time invariants only. Metadata
helpers (`ReadWorkspaceMetadata`, `WriteWorkspaceAskMetadata`,
`NetworkAskMetadata`, `NativeMetadata`) set namespace, policy tiers, approval
mode, and capabilities; native capabilities inform session approval policy and
sandbox planning. Convention: each tool lives in
`tools/system/<tool>/<name>.go` with a `Register…Tool` function wired into
`load_system_tools.go`.

## The native tool set (one line each)

| Tool | What it does |
|---|---|
| `tool_search` | Search the registry; `include_schema=true` unlocks deferred tools |
| `apply_patch` | Reviewable multi-file patches (Add/Update/Delete), dry-run, rollback, sensitive-path approval |
| `file_search` | Filename/literal/regex search over workspace files (git-aware, scored) |
| `web_search` | Brave/Tavily/custom HTTP/SearXNG (auto-started local container; `INVOKR_WEB_SEARCH_DISABLED=1` opts out) providers, merge+dedupe+rank, cached; sensitive queries need approval |
| `web_fetch` | Fetch one http(s) URL as readable text; size cap + truncation marker, redirect limit, private/loopback hosts refused at dial time; always asks on the `web_network` tier |
| `retrieval` | Lexical(+vector) search over an on-disk index of project docs and memory |
| `memory` / `recall` | CRUD + search over persistent memory records (no approval by design) |
| `recall_archive` | Search/restore *forgotten* memory; always approval-gated |
| `session_search` | Search / list / load prior session transcripts |
| `load_skill` | Load a skill's full instructions |
| `delegate_task` | Bounded child agents ([11-subagents](11-subagents.md)) |
| `shell` + `process` | Allowlisted argv-only commands; background process control |
| `code_interpreter` | Python in a container (preferred) or namespace-sandboxed process, staged inputs, artifacts |
| `tmux` | Drive interactive TUIs in a process-private tmux server |
| `create_tool` | Author a new Python toolbox plugin at runtime |
| `schedule` | Manage cron jobs ([13-jobs](13-jobs.md)) |
| `mcp_<server>_<tool>` | Tools discovered from configured MCP stdio servers |


### `web_fetch` SSRF guard (dangerous escape hatch)

`web_fetch` installs a `net.Dialer.Control` hook on its transport, so every
connection — the first request and every redirect hop — is checked *after* DNS
resolution and *before* connect. Loopback, RFC1918, CGNAT (`100.64.0.0/10`),
benchmark (`198.18.0.0/15`), link-local (incl. `169.254.169.254`), multicast and
unspecified addresses are refused. A URL check also runs pre-flight, but only for
a clearer error message; the dial hook is the security boundary.

Caveat: when `HTTP_PROXY`/`HTTPS_PROXY` is set the dial goes to the *proxy*, so
that is the address the hook sees. A proxy on a private address is therefore
refused, and a public proxy is trusted not to relay into private space — only
the pre-flight URL check applies to the target in that case.

`web_search.allow_private_hosts` / `INVOKR_WEB_FETCH_ALLOW_PRIVATE_HOSTS=1`
**turns that guard off completely** and is the only thing that does. It exists
for hermetic e2e fixtures and self-hosted setups that must reach an internal
address. Never set it on a machine where an agent handles untrusted input: with
it on, a model-chosen URL can reach cloud metadata endpoints and internal
services. It is unrelated to the `web_search.private_hosts` redaction list.

## Workspace policy (`tools/system/workspace`)

`Policy` normalizes a primary root (default: process CWD) + extra roots.
`Resolve(path)` makes paths absolute, resolves symlink prefixes (loop-capped),
and rejects anything outside the roots. `AllowsHiddenPath` blocks dot-path
components unless `workspace.allow_hidden_paths`. Every filesystem-touching
tool goes through it — use it, don't reimplement.

## Shell policy (`tools/system/shell`)

- Argv-only (`exec.Command`, no shell interpretation). Allowlist default:
  `ls, git, pwd, go, gofmt`.
- Two model-miscall recoveries: a whole command line in `"command"` is split
  (unless quoted), and a duplicated executable as args[0] is dropped.
- Tier classification per command/subcommand: `execute_readonly_command`
  (e.g. `git status/diff/log`, `go vet`, `gofmt` without `-w`) runs without
  approval; write and network tiers ask unless
  `shell.allow_write_commands`/`allow_network_commands`.
- Env is isolated by default: only `PATH HOME LANG TERM TMPDIR` +
  `env_passthrough` — host secrets stay invisible.
- `background=true` returns a `process_id`; the `process` tool
  lists/polls/waits/reads-log/kills, scoped to the calling session, logs under
  `~/.invokr/process/`, 24h/100-entry retention.
- Non-zero exit is a *flagged* result (`ToolError{Code:"exit N", Payload:…}`)
  so the model sees stdout/stderr.

## Result filtering (tokf)

If the `tokf` binary is present and not disabled, every tool result is piped
through `tokf run <invokr-agent> tokf-filter-stdin --tool …` (see
`tools/integrations/tokf_filter.go`). Failure of the filter logs a warning and
passes the result through unfiltered. Disabled automatically inside `go test`
binaries.
