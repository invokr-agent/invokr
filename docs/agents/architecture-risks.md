# Invokr Architecture Risks

Use this as architecture review backlog, not as proof the design is broken.

## Security Surfaces

Workspace paths, shell execution, dynamic plugins, browser automation, web search, and plugin dependency preparation are the highest-risk areas. Keep workspace policy, shell tiering, explicit env controls, and audit events visible when changing these modules.

## Config Scale

`config.AppConfig` owns many concerns: provider, agent, memory, workspace, toolbox, skills, shell, web search, logging, defaults, and env parsing. New options should be grouped in existing subconfigs when possible and tested through defaults/env/config-file paths.

## Memory Manager Density

`capability/memory/manager.go` coordinates Markdown, SQLite, recall, events, llama summarization, and vector storage. It has useful leverage, but it is dense. Prefer extracting behind existing memory interfaces only when locality improves, not for cosmetic file splitting.

## Provider Coupling

OpenAI SDK shapes leak through provider and agent-adjacent code. The current adapter is pragmatic for OpenAI-compatible local servers. New provider features should avoid spreading SDK-specific types further into agent or prompt modules.

## Prompt Budget Realism

Prompt assembly and memory curation can be character-oriented while runtime accounting is provider-token based. Changes near prompt budgeting should check actual provider context-window metadata and model token usage behavior.

## Synchronous Events

Ordered events give deterministic behavior, but slow handlers can block agent progress. Async dispatch exists, but runtime uses ordered dispatch. Any handler doing network, process, or heavy storage work needs extra scrutiny.

## Test Gaps To Consider

- race/concurrency tests for events, skills cache, and tool refresh
- integration tests for full agent/tool/memory flow
- fuzz tests for syntax parsing and config/env parsing
- security tests around path normalization, hidden paths, shell classification, and plugin env isolation
