# Built-in memory models (Zerfoo)

Invokr's memory **embeddings** and **vector recall** run entirely in-process on
a Go model runtime, [github.com/zerfoo/zerfoo](https://github.com/zerfoo/zerfoo).
It loads GGUF models directly — **no `llama-server`, no sidecar process, no CGo,
no Python.**

**Summarization does not run in-process.** Zerfoo's CPU autoregressive decode is
too slow (~17 s/token; a single summary hung >10 min), so summaries are generated
by the **main chat model** Invokr already talks to (the OpenAI-compatible
provider) via `capability/memory/chat_summarizer.go`. The in-process Zerfoo
generator remains only for the gated integration tests. Embeddings (a single
forward pass, no decode loop) are fast and stay in-process.

## Architecture

Small Invokr-owned interfaces keep Zerfoo types from leaking across packages
(`capability/memory/zerfoo`):

- `Embed` / `EmbedDocuments(ctx, []string)` — embed stored memory passages.
- `EmbedQueries(ctx, []string)` — embed search queries (query semantics).
- `Summarize(ctx, summary.StructuredSummaryInput)` — produce a faithful,
  JSON-structured memory summary.
- `Close()` — release loaded models.

The `summary` package (`capability/memory/summary`) holds the model-agnostic
summary types, prompt, and parser shared by the worker and the memory manager.

Models load **lazily** on first use and the loads are concurrency-safe, so
constructing a worker (or running unrelated commands/tests) loads nothing.

## Models

### Embeddings — `Qwen/Qwen2.5-0.5B-Instruct-GGUF/Q8_0` (default)

- Used for memory embeddings and vector recall.
- **Why Qwen2.5:** the model GGUF must be one of Zerfoo's registered
  architectures (`qwen2`, `llama`, `gemma`, `phi3`, `mistral`, … are safe small
  families). LFM2 GGUFs fail Zerfoo's graph builder (`missing tensor
  model.norm.weight`) and `qwen3` is unsupported. See follow-ups issues K/L.
- Expected dense output: **896-dim** (Qwen2.5-0.5B hidden size). Invokr enforces the
  configured `embedding_dimensions` and rejects mismatched vectors before
  storage, which also handles schema/dimension migrations safely (old vectors of
  a different dimension are not used for ranking against new ones).
- **Normalization:** Zerfoo's `Embed` mean-pools token embeddings and returns
  **L2-normalized** vectors, so Invokr does no extra normalization; cosine
  similarity over them is well-defined. This is asserted by the integration
  test (`|v| ≈ 1`).
- **Asymmetric query/document:** queries are embedded with a `query: ` prefix
  (the model's instruction convention); documents are embedded raw. Because
  Zerfoo's embedding is mean-pooled rather than a full instruction-tuned forward
  pass, the prefix effect is modest, but the two paths are kept genuinely
  distinct via `EmbedQueries` vs `EmbedDocuments`.

### Summarization — the main chat model (not Zerfoo)

Summaries are produced by the OpenAI-compatible chat provider Invokr is already
configured with (`provider.base_url`), not by an in-process Zerfoo generator. The
same summary prompt (`capability/memory/summary`) is rendered and sent as a JSON
chat completion, then parsed; generated summaries are embedded (in-process) and
stored as memory documents. This runs even when Zerfoo is disabled — only
embeddings/vector recall need Zerfoo.

The `summarization_model` config field below is still honored by the **gated
integration tests** (which exercise Zerfoo's generator directly), but it has no
effect on the normal runtime path. If you do run it, pick a small GGUF in a
Zerfoo-supported arch (e.g. `Qwen/Qwen2.5-1.5B-Instruct-GGUF`); LFM2 and
encoder-decoder models (BART/T5) do not load. See follow-ups issues I/K/L/M.

## Configuration

```json
{
  "memory": {
    "zerfoo": {
      "enabled": true,
      "embedding_model": "Qwen/Qwen2.5-0.5B-Instruct-GGUF/Q8_0",
      "summarization_model": "Qwen/Qwen2.5-1.5B-Instruct-GGUF/Q4_K_M",
      "embedding_path": "",
      "summarization_path": "",
      "embedding_dimensions": 896,
      "max_summary_tokens": 512,
      "max_raw_events_before_summary": 12,
      "min_importance_to_store": 0.35,
      "request_timeout_ms": 120000
    }
  }
}
```

- Model fields accept HuggingFace IDs (`owner/model` or `owner/model/QUANT`,
  default quant `Q4_K_M`) or, via the `*_path` fields, local GGUF paths that take
  precedence for offline use.
- **Cache directory:** downloaded models are cached under
  `~/.cache/zerfoo/models`. Use the `*_path` overrides to run fully offline.

Environment overrides mirror the JSON keys: `INVOKR_MEMORY_ZERFOO_ENABLED`,
`INVOKR_MEMORY_ZERFOO_EMBEDDING_MODEL`, `INVOKR_MEMORY_ZERFOO_SUMMARIZATION_MODEL`,
`INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH`, `INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH`,
`INVOKR_MEMORY_ZERFOO_EMBEDDING_DIMENSIONS`, `INVOKR_MEMORY_ZERFOO_MAX_SUMMARY_TOKENS`,
`INVOKR_MEMORY_ZERFOO_MAX_RAW_EVENTS_BEFORE_SUMMARY`,
`INVOKR_MEMORY_ZERFOO_MIN_IMPORTANCE_TO_STORE`, `INVOKR_MEMORY_ZERFOO_REQUEST_TIMEOUT_MS`.

## Recall gate

The recall gate is an optional, off-by-default feature that makes first-turn
memory injection conditional. When a session opens with a message, the gate
sends one cheap, thinking-suppressed ("nothink") probe completion through the
chat provider that classifies whether long-term recall would help and, if so,
emits targeted search queries. The gate then runs tiered recall for those
queries and injects the hits as a `Relevant Memory` block into the first turn's
system prompt. If the probe says no — or fails, times out, or returns garbage —
the turn proceeds exactly as without the gate. Later turns rely on the recall
tool as usual (the probe fires on the first turn only).

The probe reuses the provider's one-shot `Summarize` path (JSON, no tools,
capped at `RecallGateMaxTokens` output tokens). Every failure mode degrades to
"no recall block", never a failed turn; the only added cost is the probe's
latency, bounded by `timeout_ms`. The gate is wired for the primary session
only — subagents skip it.

## Forgetting (opt-in decay + archive)

By default, persistent memory only accumulates. The **forgetting** extension
adds an Ebbinghaus-style retention model so memories fade unless they are used:
`retention = strength * exp(-ln(2) * age / half_life)`, where `age` counts from
the last recall (falling back to the last update). Each recall reinforces a
record (bumps `recall_count`, resets `last_recalled_at`, scales `strength` by
`reinforcement_factor` up to `max_strength`), so frequently recalled memories
become effectively permanent while unused ones decay.

It is **disabled by default**. When off, memory behavior is byte-for-byte
identical to before — zero extra writes, zero score changes, zero sweeps.

### What forgets, what never forgets

- **Forgets:** only `source = runtime` SQLite records (facts, summaries,
  episodes) and old raw turns. When a record's retention drops below
  `min_retention` it is **archived**, not deleted — it moves to cold storage
  that no automatic recall path (prompt blocks, `recall` tool, raw-event
  fallback) ever reads. Optional `purge_after_days > 0` hard-deletes archived
  rows (and their vectors) after that many days; the default `0` keeps the
  archive forever. Raw turns older than `raw_event_retention_days` that precede
  the last summarization checkpoint are pruned.
- **Never forgets:** markdown memory (`AGENTS.md` / user / project docs) is
  user-curated and pinned. Records whose kind is in `exempt_kinds` (default
  `preference`, `profile`) never decay. `decision` records decay like facts.

### The archive and `recall_archive`

Forgotten records are recoverable only through the `recall_archive` tool, which
searches the archive and restores selected records to active memory (strength
reset to 1.0 — a restored memory re-earns its persistence). Unlike `memory` and
`recall`, `recall_archive` requires **explicit user approval on every call**, so
the model can never silently browse forgotten memory, and under an unattended
deny policy the archive is unreachable.

The decay sweep is schedulerless: it piggybacks on the existing summarization
checkpoint (every `max_raw_events_before_summary` turns) and on manager
shutdown. There is no daemon, cron, or timer.

### Configuration

```json
{
  "memory": {
    "recall_gate": {
      "enabled": false,
      "model": "",
      "nothink_suffix": "/no_think",
      "max_queries": 3,
      "per_query_limit": 5,
      "max_hits": 8,
      "budget_chars": 2000,
      "min_message_chars": 12,
      "timeout_ms": 10000
    },
    "forgetting": {
      "enabled": false,
      "half_life_days": 14,
      "min_retention": 0.15,
      "reinforcement_factor": 1.5,
      "max_strength": 10.0,
      "purge_after_days": 0,
      "raw_event_retention_days": 7,
      "exempt_kinds": ["preference", "profile"]
    }
  }
}
```

**Recall gate:**

- `model` empty falls back to `provider.model` (same rule as summarization).
- `nothink_suffix` empty disables the suppression token; it rides in the prompt
  text, so no provider API change is needed. Models that ignore it are bounded
  by the output-token cap, and a leading `<think>…</think>` block is stripped
  during parsing.
- `min_message_chars` skips the probe for short greetings and one-word commands.
- Environment overrides mirror the JSON keys:
  `INVOKR_MEMORY_RECALL_GATE_ENABLED`, `INVOKR_MEMORY_RECALL_GATE_MODEL`,
  `INVOKR_MEMORY_RECALL_GATE_NOTHINK_SUFFIX`,
  `INVOKR_MEMORY_RECALL_GATE_MAX_QUERIES`,
  `INVOKR_MEMORY_RECALL_GATE_PER_QUERY_LIMIT`,
  `INVOKR_MEMORY_RECALL_GATE_MAX_HITS`,
  `INVOKR_MEMORY_RECALL_GATE_BUDGET_CHARS`,
  `INVOKR_MEMORY_RECALL_GATE_MIN_MESSAGE_CHARS`,
  `INVOKR_MEMORY_RECALL_GATE_TIMEOUT_MS`.

When it fires, the gate dispatches a `notice` event ("checking memory…") so the
TUI can show why the first token is late, and the recall it runs emits the usual
`before_recall`/`after_recall` events, so memory panes see gate-driven recall
like tool-driven recall.

**Forgetting:**

Environment overrides mirror the JSON keys (`INVOKR_MEMORY_FORGETTING_ENABLED`,
`INVOKR_MEMORY_FORGETTING_HALF_LIFE_DAYS`, etc.), same pattern as
`INVOKR_MEMORY_ZERFOO_*`. `WithDefaults()` fills every zero value, so an empty
block with `"enabled": true` is a valid configuration.

**To disable:** leave `enabled` at `false` (the default), or set
`INVOKR_MEMORY_FORGETTING_ENABLED=false`.

## Fallback

When built-in memory is disabled, or a model fails to load, recall falls back to
SQLite full-text search (FTS). Vector recall is additive, not required.

## Migration from `memory.llama.*`

The llama.cpp/`llama-server` runtime has been removed. Replace old config:

| Old | New |
|---|---|
| `memory.llama.enabled` | `memory.zerfoo.enabled` |
| `memory.llama.model_path` | `memory.zerfoo.embedding_path` / `summarization_path` |
| `memory.llama.vector_dimensions` | `memory.zerfoo.embedding_dimensions` |
| `memory.llama.max_summary_tokens` | `memory.zerfoo.max_summary_tokens` |
| `memory.llama.max_raw_events_before_summary` | `memory.zerfoo.max_raw_events_before_summary` |
| `memory.llama.min_importance_to_store` | `memory.zerfoo.min_importance_to_store` |
| `memory.llama.request_timeout_ms` | `memory.zerfoo.request_timeout_ms` |
| `INVOKR_MEMORY_LLAMA_*` | `INVOKR_MEMORY_ZERFOO_*` |

`binary_path`, `host`, `port`, `context_size`, `threads`, `parallel`,
`gpu_layers`, `pooling`, and `extra_args` are gone — there is no server to
configure. Stale `memory.llama.*` keys in an existing config file are ignored.

## Tests

Fast offline unit tests run by default:

```bash
go test ./...
```

Model-loading integration tests are gated by the `integration` build tag **and**
`INVOKR_ZERFOO_INTEGRATION=1` (so the default suite stays fast and offline):

```bash
INVOKR_ZERFOO_INTEGRATION=1 go test -tags=integration ./capability/memory/...
```

Optionally avoid downloads by pointing at local GGUF files (or override the
default model IDs):

```bash
INVOKR_ZERFOO_INTEGRATION=1 \
INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH=/path/embed.gguf \
INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH=/path/sum.gguf \
go test -tags=integration ./capability/memory/...
```

### Where downloaded models are cached

With no explicit path or model ID, the tests use the documented default model
IDs and download them on first run. `support/testutil.UseRepoModelCache` points
that download at `.modelcache/` in the repo (gitignored), so the GGUF is fetched
once and reused by every later run.

Model selection order (deterministic, first match wins):

1. `INVOKR_MEMORY_ZERFOO_EMBEDDING_PATH` / `INVOKR_MEMORY_ZERFOO_SUMMARIZATION_PATH`
   — explicit local GGUF files.
2. `INVOKR_MEMORY_ZERFOO_EMBEDDING_MODEL` / `INVOKR_MEMORY_ZERFOO_SUMMARIZATION_MODEL`
   — explicit Hugging Face model IDs.
3. Otherwise the documented default model IDs, downloaded into `.modelcache/`.
