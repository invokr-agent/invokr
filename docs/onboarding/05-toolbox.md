# 05 — Toolbox Plugins & Sandboxing

Packages: `tools/system/toolbox/`, `tools/system/create_tool/`,
`toolbox/` (embedded defaults), `capability/docker_sandbox/` (whole-agent docker).
ADRs: 0002 (sandboxed agent-authored tools), 0005 (docker vs namespace
sandbox), agents/adr 0005 (namespace sandbox + supply chain).

## Plugin anatomy

A plugin is a directory under `~/.invokr/toolbox/<name>/`:

- `tool.json` (or `plugin.json`/`hamr.json`): `name`, `description`, `example`,
  `parameters` (JSON Schema), `policy` (`tiers` + `approval`), `capabilities`
  (reads/writes workspace, network, executes code), optional `always_visible`,
  optional `daemon`.
- Exactly one entrypoint: `main.go` (run via `go run`), `main.js` (node), or
  `main.py` (python). The runtime is inferred from which file exists.
- Working directory: a plugin declaring a workspace capability runs with cwd =
  the workspace root, so relative path arguments mean what they do everywhere
  else (#535); its own directory is in `INVOKR_TOOL_DIR`. Go plugins are the
  exception — `go run` needs its module context, so they keep cwd = the plugin
  dir and must use `INVOKR_WORKING_DIR` or absolute paths for workspace files.
- Protocol: one JSON call envelope (`{"name":…,"arguments":{…}}`) on stdin →
  plain text or `{"result":…,"error":…}` on stdout. Daemon mode keeps the
  process alive and speaks newline-delimited JSON per call, one daemon per
  (plugin, session), with a state dir passed via `INVOKR_TOOL_STATE_DIR`.

Discovery: `RegisterToolboxTools` scans the dir at bootstrap and re-scans on
`EventToolboxRefreshRequested` (fired by `tool_search` and `create_tool`),
debounced by dir mtime. Plugins register as native tools under source
`"toolbox"`; names colliding with true native tools are skipped. Approval is
enforced per the spec's `policy.approval` before every call.

The repo ships one example plugin, `toolbox/browser_agent` (Python); there is
no bundled-defaults mechanism — file reading is the native `read_file` tool
(`tools/system/read_file/`), not a toolbox plugin.

## The namespace sandbox (Linux)

Every plugin invocation runs under `unshare --user --map-root-user --mount
--pid --fork --uts --ipc` (+`--net` unless the tool declares network), inside a
from-scratch root built by bind-mounting only what the tool needs *at its
original absolute paths*: `/usr /bin /lib /lib64`, the tool dir, the resolved
interpreter's dir, the venv (python), and the workspace RO or RW per declared
capabilities. The pivot_root idiom (`pivot_root . .` + lazy umount) is the only
sequence validated not to leak the host root — don't "simplify" it. Memory is
bounded via `ulimit -v`; wall clock via `toolbox.exec_timeout_ms` (60s
default).

If user namespaces don't work (probed once at init), execution is **refused**
unless `INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1`. Non-Linux always needs the
opt-out. There is no silent fallback.

## Python supply chain (M3)

`requirements.txt` must be exactly pinned `name==version` (extras/markers ok;
no options, URLs, VCS, or path refs). Install flow
(`supplychain.go`):

1. Resolve with `pip install --dry-run --report` behind a localhost proxy that
   only allows `pypi.org`/`files.pythonhosted.org` (configurable).
2. Validate the report: exact name/version/URL/sha256, allowed artifact host,
   release age ≥ `release_cooldown_days` (14) via PyPI metadata.
3. Ask the user to approve the resolved package list.
4. Write `requirements.lock` with hashes; `pip download --require-hashes`;
   `pip install --no-index` from the downloaded wheels. Wheel-only everywhere
   (`--only-binary=:all:`) so sdist build scripts never execute.

Each plugin gets its own `.venv` (shared only via the explicit
`toolbox.venv_dir` override); a sha256 marker skips reinstalls, and a changed
requirements file rebuilds the venv rather than installing additively.
Install-time code runs inside the sandbox with network allowed but the
filesystem confined to the venv.

## `create_tool`

Lets the model author a Python plugin: validates the name/spec/requirements
with the *same* gates the loader uses, writes to a staging dir, `ValidateToolDir`
proves it loads, then swaps it into place (backup + rollback on overwrite).
Always approval-gated; fires a toolbox refresh so the tool is callable in the
same session.

## Whole-agent docker sandbox (`capability/docker_sandbox`)

With `sandbox.enabled` (or `--sandbox` / `INVOKR_SANDBOX=1`), `main.go`
re-execs the current binary inside docker/podman before bootstrap: workspace
and `~/.invokr` are bind-mounted, env is filtered to `INVOKR_*` +
`OPENAI_API_KEY` + terminal vars, and a loopback provider URL is rewritten to
`host.docker.internal`. The image is auto-built from the running binary
(Linux hosts; digest-pinned debian base) and tagged by binary hash unless
`sandbox.image` pins one. Inside the container the namespace sandbox is
disabled (`INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1`) — the two are alternatives,
not layers (ADR 0005).

## Gotchas

- The plugin env is isolated by default (`toolbox.isolate_env`), which means
  **no PATH** for unsandboxed runs; the sandbox script sets its own PATH.
  Tools spawning subprocesses should not rely on host PATH.
- `GOCACHE` is bind-mounted RW into the sandbox (else `go run` recompiles the
  world every call) and `GOROOT` is resolved on the host and passed through.
- Retired tools (`write_file`, `replace_in_file`) are refused by the loader —
  `apply_patch` replaced them.
