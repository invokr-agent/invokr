# Toolbox HAMR Plugins

This directory contains HAMR plugins for the Invokr agent. `tools.Caller` loads
them dynamically from `toolbox/*` and exposes them as tools during an agent turn.

Each plugin lives in its own directory and must include:

1. `tool.json` (or `plugin.json` / `hamr.json`)
2. one runtime entrypoint file

The built-in plugins are intentionally small Python programs named `main.py`.

## Spec format

```json
{
  "name": "my_tool",
  "description": "What the tool does",
  "example": "{\"name\":\"my_tool\",\"arguments\":{\"key\":\"value\"}}"
}
```

### Optional fields

All three are optional; a manifest omitting them behaves exactly as before.

- `parameters` — a JSON Schema object for the tool's arguments. It reaches the
  model in both the native OpenAI tool definitions and the tagged-JSON tool
  listing, and arguments are validated against it **in-core**: a violating call
  returns a flagged tool error with the validation message and the plugin
  process is never started.

  **Validation is shallow, and it fails open on everything it does not
  understand.** Exactly four things are enforced, all at the top level of the
  argument object: `type: "object"` (a schema whose top-level `type` is anything
  else is skipped entirely), `required`, each supplied property's `type` and
  `enum`, and `additionalProperties: false`.

  Everything else is accepted without being checked — a call violating it still
  reaches the plugin, which must validate it itself. That includes: `required`,
  `properties` and `additionalProperties` on a *nested* object (there is no
  recursion — only the top level is walked), `items` and any other array
  constraint, a type union such as `["string", "null"]`, `oneOf`/`anyOf`/
  `allOf`/`not`, `$ref`, `const`, `default`, and every numeric/string
  refinement (`minimum`, `maximum`, `multipleOf`, `minLength`, `maxLength`,
  `minItems`, `pattern`, `format`). A `type` keyword outside the JSON type set
  (`"int"`, `"float"`, a typo) matches no value and so rejects every call —
  use `"integer"` and `"number"`.

  The point of the check is to catch the calls a model gets wrong most often
  (a missing or misspelled argument, a wrong primitive type, a value off the
  enum) before a process is spawned. It is not a security boundary: the schema
  is written by the plugin's own author, so a hole in it can only widen what
  that plugin accepts, never what the sandbox permits.
- `limits: { timeout_seconds, memory_mb, output_kb }` — per-tool overrides of
  the 60 s / 2 GiB RLIMIT_AS / 1 MB defaults. Each is bounded by a config cap
  (`toolbox.max_limit_timeout_ms`, `max_limit_memory_bytes`,
  `max_limit_output_bytes`); a value over its cap makes the plugin fail to load
  with a diagnostic rather than being silently clamped.
- `network: { allow_hosts: [...] }` — scoped network at exec. Declaring it
  turns on the sandbox's network namespace, adds the `execute_network_command`
  approval tier (so the call stays approval-gated and auditable), and gives the
  sandbox a generated `/etc/hosts` holding only the listed hosts with no
  `/etc/resolv.conf` — an undeclared name has nowhere to resolve. Scoping is by
  name resolution, so it does not stop a plugin that dials a literal IP.

  Declaring `capabilities.network: true` with no `network` block still means
  what it always did — an *unscoped* namespace with the host resolver — and it
  now takes the same `execute_network_command` tier and the same approval lift
  (`auto` becomes `ask`; an explicit `never` is left alone). The broader route
  must not be the cheaper one.

### Progress output

A plugin may write incremental status to **stderr** as lines prefixed with
`INVOKR_PROGRESS:`. They are forwarded to the tool event bus and rendered as
live progress in the TUI and in `invokr exec` (`item.progress` under `--json`).
Every other stderr line, and all of stdout, keeps its existing meaning, so the
final `{"result":...}` / `{"error":...}` payload is unaffected.

Progress is a **one-shot-plugin feature only**. A plugin declaring
`"daemon": true` keeps one long-lived process per session and multiplexes every
call over its stdio, so its stderr is not attributable to a single call and is
not teed into the event bus; progress lines it writes are ignored. A daemon
plugin that needs to report incremental status must either drop `daemon` or
return the status in its final payload.

## Runtime execution

- Python: `python3 main.py`

Each Python plugin gets its own virtual environment at `<plugin-dir>/.venv`
(not under this repo directory). The agent creates it on demand, installs the
plugin's `requirements.txt` into it when that file changes, and runs the
plugin with that venv's Python. A separate venv per plugin means two plugins
can pin different versions of the same package without colliding; if a
plugin's `requirements.txt` changes, its venv is rebuilt from scratch (not
just `pip install`'d on top) so a removed dependency is actually dropped.

Use `INVOKR_PYTHON_BIN` to choose the Python executable used to create
plugin venvs. Use `INVOKR_TOOLBOX_VENV_DIR` to force every plugin onto one
shared venv at a fixed path instead (overrides the per-plugin default). Use
`INVOKR_PYTHON_PIP_ARGS` to override the pip arguments placed before the
requirements path (defaults to `install --only-binary=:all: -r`, i.e.
wheel-only installs — pip never executes a sdist's build backend).

### Requirements pinning

Every line in a plugin's `requirements.txt` must be an exact pin
(`name==version`, optionally with extras: `name[extra]==version`). VCS/URL
specs, `path` specs, unpinned names, and pip option lines (e.g. `-r`, `--hash`)
are all rejected before install. Package names are also checked against
PyPI's JSON metadata endpoint on first install, so a hallucinated or
typosquatted name is refused before `pip` ever runs. Set
`INVOKR_TOOLBOX_PKG_ALLOW`/`INVOKR_TOOLBOX_PKG_DENY` (comma-separated) to
further restrict which package names may be installed at all.

### Sandboxing

On Linux, both venv creation/install and plugin execution run inside a
Linux-namespace sandbox (`unshare`/`pivot_root`), confined to the plugin's
own directory (which contains its venv) and, if the tool declares
`writes_workspace`, the workspace folder. Install runs with network access;
execution runs with network off by default. Set
`INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1` to bypass sandboxing (not recommended;
unsupported on non-Linux).

Override toolbox root with:

- `INVOKR_TOOLBOX_DIR`
- `INVOKR_TOOLBOX_ISOLATE_ENV`
- `INVOKR_TOOLBOX_ENV`
- `INVOKR_GO_CACHE_DIR`
- `INVOKR_TOOL_STATE_DIR` (per-plugin, per-session daemon state root)
- `INVOKR_BROWSER_AGENT_STATE` (legacy browser-agent state-root override)

## Agent-authored tools

The `create_tool` native tool lets the agent write a new toolbox plugin
(`tool.json`/`main.py`/`requirements.txt`) at runtime and makes it callable
immediately by triggering a toolbox refresh — no restart required. Validation
and execution go through the same spec checks, requirement pinning, package
policy, and sandboxing as any other toolbox plugin; `create_tool` itself is
gated behind approval.

Shell command execution is not a toolbox plugin. It is registered as a native
tool from top-level `shell` config and defaults to `ls`, `git`, `pwd`, `go`,
and `gofmt`. Use `shell` for simple Unix commands; reserve toolbox plugins for
workflows that need Python, Node, or Go code.

## I/O contract

Input on stdin:

```json
{"name":"tool_name","arguments":{}}
```

Output on stdout:

```json
{"result":"tool output"}
```

or

```json
{"error":"failure details"}
```

Invokr parses this stdout contract before applying optional tokf filtering.
Filtering is only applied to successful `result` payloads, so tokf cannot break
plugin JSON parsing.
