# ADR 0003: Memory Recall Tiers

## Status

Accepted.

## Context

Invokr needs stable project/user facts, runtime memory records, and optional richer local recall without requiring every install to run llama.cpp.

## Decision

Use layered memory:

- Markdown for pinned user/project/agent facts and prompt blocks.
- SQLite records and FTS5 for runtime memory and lexical recall.
- Optional local llama runtime for summarization, embeddings, and vector recall.

`TieredManager` (named `MinimalManager` when this ADR was written) coordinates storage, recall, prompt blocks, after-response curation, and optional vector/summarization work. The recall orchestrator applies policy across pinned and lexical/vector sources.

## Consequences

- Basic memory works without llama.cpp.
- Vector recall is opt-in through config/setup.
- Markdown remains appropriate for stable, human-editable facts.
- Memory manager is intentionally high-leverage but dense; split only when locality improves behind existing interfaces.
