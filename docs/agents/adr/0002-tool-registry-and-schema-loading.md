# ADR 0002: Tool Registry And Schema Loading

V5 supersedes schema loading/visibility with [ADR 0020](../../adr/0020-managed-tools.md).

## Status

Accepted.

## Context

Invokr supports native tools and dynamically discovered toolbox executable tools. It also needs progressive disclosure so every tool schema does not always enter the prompt.

## Decision

Use `tools.Caller` as the central registry for specs, native handlers, and per-session schema loading. Tools can be visible by name/description while full schemas are loaded through `tool_search`.

Toolbox executable tools are registered by `tools/system/toolbox` as source-scoped native handlers with `Metadata.Source == "toolbox"`. Refresh replaces only that source so stale toolbox tools are removed without touching built-in native tools.

Tool calls route through the registry in this order:

1. schema visibility check
2. native handler
3. unknown-tool error

## Consequences

- Native and toolbox executable tools share one `ToolSpec` and call shape.
- Prompt size can stay smaller through schema search/loading.
- Native tools can intentionally override toolbox names, such as the native `shell` tool.
- Changes to visibility, schema loading, or call routing should be tested at the registry level.
