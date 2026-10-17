# 06 — Memory

Packages: `capability/memory/` (+ subpackages `core`, `markdown`, `sqlite`,
`recall`, `policy`, `summary`, `zerfoo`, `recallgate`, `forgetting`,
`history`), `capability/sessionstore/`. Reference: `docs/memory.md`,
agents/adr 0003.

## Three storage tiers

1. **Pinned markdown** (`markdown/`) — `USER.md`, per-workspace `MEMORY.md` (persona lives in `PERSONA.md`, the base system prompt)
   under `~/.invokr/`, char-limited (32000/1375/2200), one bullet per entry,
   atomic writes under a flock. Always injected into the prompt (subject to
   budget). Near-duplicate entries are rejected by word-set Jaccard ≥ 0.8.
2. **SQLite records** (`sqlite/`, `memory.db`) — runtime records
   (scope/kind/content/summary), FTS5 index for lexical recall, and a
   `memory_vectors` table for embeddings. Goose migrations; `quick_check` +
   required-table verification at open; soft delete via `status`.
3. **Raw memory events** (`raw_memory_events`) — verbatim user/assistant turns,
   the substrate for summarization and the early-recall fallback.

`TieredManager` (`manager.go`) fronts all of it and implements
`memory.Manager`. Subagents get `NoopManager` — they never read or write
memory directly.

## The write path (event-driven)

`EventAfterResponse` → `curator.HandleEvent` (`curator.go`):

1. Append raw events (user + assistant) to sqlite **and** mirror them into the
   JSONL session transcript (so `session_search` sees every frontend).
2. Every `max_raw_events_before_summary` (12) events: enqueue a summarization
   job on the curator's single background worker (bounded queue of 8; full →
   drop, next threshold retries). The summarizer renders a JSON-only prompt
   (`summary/`), calls the **chat model** (`chat_summarizer.go` — Zerfoo decode
   is too slow), parses forgivingly, and stores a `summary` record plus
   `fact` records (confidence ≥ 0.5, importance ≥ 0.35).
3. Run the forgetting sweep (no-op when disabled).

Subagent turns (`ParentSessionID != ""`) are skipped; `delegate_task` records
its input/output explicitly instead.

The curator does **not** write pinned markdown. USER.md/MEMORY.md change only
through an explicit `memory` tool call with `source: markdown`; the keyword
auto-curation heuristic that used to promote `i prefer…`/`we use…` sentences
was removed (issue #317 B10, after the assistant-prose half went in #299).

## The read paths

- **Prompt blocks** (`PromptBlocks`): pinned markdown blocks (+ optional tier-2
  hits when policy enables prompt injection — currently off in
  `policy.Default`), budgeted to 6000 chars, fenced in `<recalled-memory>`.
- **Recall** (`Recall`): orchestrator (`recall/orchestrator.go`) runs the
  lexical/vector source over `memory_records` (so user- and project-scoped
  saved records surface, not just transcripts), appends matching pinned
  markdown entries, dedupes by record key. A `session_id` on the query is a
  preference, not an exclusion: records with no session still match. Empty result → raw-event fallback: recent turns from this session
  (or, for a fresh session, recent cross-session turns ≤ 30 days, marked
  `cross-session context`), filtered by query terms, scored 0.1.
- **Vector recall**: when a Zerfoo worker exists, queries are embedded
  (`query: ` prefix) and ranked by cosine against stored vectors
  (`sqlite.RecallVector`, candidate cap 2000); otherwise FTS5 (bm25).
- **Retention re-rank**: with forgetting enabled, hit scores are multiplied by
  the decay curve and recalled records are reinforced (`TouchRecall`).

## Forgetting (opt-in, `memory.forgetting.*`)

Pure math in `forgetting/retention.go`: retention =
`strength * exp(-ln2 * ageDays / half_life)` (half-life 14d default), anchored
to last recall. Recall multiplies strength ×1.5 (cap 10). Below
`min_retention` (0.15) a sweep archives the record (status `archived`, out of
FTS and every recall path); archived rows can be purged after
`purge_after_days`; summarized raw events are pruned after 7 days. `preference`
and `profile` kinds never decay. The sweep piggybacks on the summarization
checkpoint and close — no scheduler. `recall_archive` (always approval-gated)
is the only reader/restorer of the archive; restore resets strength to 1.0.

## Recall gate (opt-in, `memory.recall_gate.*`)

First message of a session only: a token-capped (96) probe completion asks
"would memory recall help?" and returns JSON `{recall, queries}`. If yes, the
gate runs the queries through `Recall`, dedupes, and injects a "Relevant
Memory" block. Every failure (timeout 10s, parse error, no hits) degrades to
"no block, no error". The `/no_think` suffix suppresses thinking on models
that support it; a leading `<think>` block is stripped anyway.

## Zerfoo worker (`zerfoo/`)

Lazy, concurrency-safe GGUF loading in-process (no CGo/sidecar). Embeddings
are mean-pooled, L2-normalized, dimension-checked (default 896 for
Qwen2.5-0.5B). Inference is serialized per model (the GGUF runner is not
concurrency-safe); `Close` waits for in-flight inference. Summarize exists but
is only used by gated integration tests.

## Adjacent stores

- **Session transcripts** (`capability/sessionstore/`) — JSONL per session
  under `<memory dir>/sessions/`; powers `sessions` CLI, `session_search`,
  and resume.
- **Prompt history** (`memory/history/`) — `prompt_history` table in the shared `memory.db`, dedupes by content
  hash, feeds the TUI's ↑/↓ recall.

## Gotchas

- `memory`/`recall` tools are deliberately un-gated (no approval prompt);
  `recall_archive` is deliberately always-gated. Don't "fix" either.
- Markdown records get a stable id from `markdown.EntryID(content)` (a digest,
  prefixed `md-`), not the content itself — `Delete`/`Update` resolve it via
  `RemoveByID`/`ReplaceByID`, which still accept the legacy content-as-id form.
- `Update` **merges** onto the stored record: an omitted field means
  "unchanged", so callers must not pre-fill defaults before calling it.
- `Save` writes the record, then the vector; a vector failure rolls back the
  record (soft delete).
- Everything memory writes flows through events — if you bypass the manager,
  the curator, transcripts, and audit trail all miss it.
