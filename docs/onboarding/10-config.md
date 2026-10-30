# 10 — Configuration

Package: `config/`. The single source of truth is `config.AppConfig`.

## Layering (lowest to highest precedence)

1. **Defaults** — `withDefaults(homeDir)`: env-free, idempotent, fills every
   zero value (constants live in `constants/`).
2. **Config file** — `~/.invokr/config.json` (`LoadFile`; a missing file is
   fine).
3. **Environment** — `withEnv()`: every knob has a `INVOKR_*` var (plus
   `OPENAI_API_KEY`); parsing is forgiving (bad values keep the fallback).
4. **Flags** — Cobra binds global flags (`--base-url`, `--model`,
   `--log-*`) and the agent/exec-only session flags (`--sandbox`,
   `--context-window-tokens`, `--tools`, `--mode`, …) directly onto the
   resolved struct in `cmd/root.go`.

`InvokrHomeDir()` resolves `~/.invokr`, honoring `INVOKR_HOME` — use it
whenever you need a path default outside `config`.

With a llama.cpp server running a unified KV cache (`kv_unified = true`), every
concurrent request shares one pool, so `context_window_tokens` must leave
headroom for the summarizer and any delegate running alongside the agent turn —
pin it well under the engine's `n_ctx_slot`, or the overlap overflows the server
and both requests die (#499).

## Sections

`Provider` (base_url, api_key, model, context_window_tokens, timeouts,
retries, optional `pricing.prompt_per_1m` / `pricing.completion_per_1m` /
`pricing.cached_prompt_per_1m` — opt-in, no bundled price table; an unset
cached rate prices cache-served prompt tokens at the full prompt rate —
plus optional `prompt_cache` (`enabled`, `request_fields`) for providers
that need explicit cache markers), `Agent` (system_prompt, summarizer_model,
temperature, n_predict, max_turns, run_timeout, max_session_tokens, max_session_cost,
native_tool_calling, parallel_tool_calls, context warning/compact
percents), `Log`, `Memory` (dir + `Zerfoo`, `RecallGate`, `Forgetting`),
`Workspace` (root, extra_roots, readonly_roots, allow_hidden_paths, reads), `ApplyPatchEnabled`,
`Toolbox` (dirs, runtimes, isolation, limits, package policy), `Skills`,
`Agents` (subagent defs dir), `Shell`, `WebSearch`,
`CodeInterpreter`, `MCP` (servers map), `Sandbox`.

## Conventions

- **Tri-state booleans** use `*bool` when "unset" must differ from "false"
  (`Shell.Enabled`, `Shell.IsolateEnv`, `Toolbox.IsolateEnv`,
  `ApplyPatchEnabled`) with an `IsEnabled()`/`IsolateHostEnv()` accessor.
  Isolation defaults to **true** — unset means isolated.
- Numeric zero means "use default" (`orDefault`); durations parse with
  `time.ParseDuration` from env.
- List env vars are comma-separated (`envList`); map-shaped ones are
  `KEY=value,K2=v2` (`envMap`); `envFields` splits on whitespace.
- Secrets: `ProviderConfig` implements `String()` with
  masking so `%v` logging can't leak them; `Redacted()` backs `config show`.
- `Validate(logger)` (called at bootstrap) rejects only what defaults can't
  repair — negative timeouts/retries/turns and inconsistent compaction
  percentages — and warns (never fails) on missing configured directories.

## Adding a knob (checklist)

1. Ask first whether it must be operator-tunable at all
   (docs/agents/subsystems.md: "Avoid adding config unless behavior must be
   operator-tunable"). Constants belong in `constants/`.
2. Add the field to the right subconfig with a `json` tag +
   `omitempty`.
3. Wire the default in that subconfig's `withDefaults`.
4. Wire the env override by adding an `env:"INVOKR_<SECTION>_<FIELD>"` struct
   tag next to the `json` tag — `applyEnv` (`config/envtags.go`) walks the tree
   and applies it. Only genuinely conditional cases (tri-state `*bool` fields,
   the tokf enable/disable inversion, derived fields) stay hand-written in a
   `withEnv` method.
5. If invalid values are dangerous, extend `Validate`.
6. Test through all three paths (defaults, file, env) — see
   `config` package tests for the pattern.

## Gotchas

- `WithDefaults()` is applied defensively in many constructors
  (`NewCaller`, `NewManagerFromConfig…`, tool services) — safe because it's
  idempotent, but remember your code may see an already-defaulted copy.
- Two model paths: `provider.model` (chat) vs `memory.zerfoo.*` (embeddings).
  A summarizer override lives in `agent.summarizer_model`, and the recall
  gate has its own `memory.recall_gate.model`.
- `workspace.root` defaults to the process CWD at `withDefaults` time and is
  fixed for the session.
