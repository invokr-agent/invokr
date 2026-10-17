# ADR 0004: Workspace And Shell Policy

## Status

Accepted.

## Context

Invokr lets an agent inspect and modify a workspace. Shell and file tools are useful but security-sensitive.

## Decision

Use a workspace policy to normalize configured roots and reject paths outside allowed roots. Built-in workspace-aware tools should resolve paths through that policy.

Use a native shell tool instead of arbitrary `sh -c` execution. Shell execution is argv-only, allowlisted by executable, classified into readonly/write/network tiers, guarded by explicit config for write and network classes, and audited through events.

## Consequences

- Tool behavior is easier to reason about than free-form shell strings.
- Workspace root mistakes should fail closed.
- Hidden paths, write commands, and network commands need explicit treatment.
- New tools that touch files or execute processes should reuse these policies rather than adding local path or command checks.
