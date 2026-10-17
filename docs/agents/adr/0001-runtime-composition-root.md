# ADR 0001: Runtime Composition Root

## Status

Accepted.

## Context

Invokr needs to wire config, logging, provider, memory, prompt history, skills, tools, syntax, events, and the agent session for both TUI and plain modes.

## Decision

Use `platform/runtime/bootstrap.go` as the application composition root. It constructs concrete adapters, registers event handlers, returns a `Services` bundle to command/UI code, and owns cleanup for runtime resources.

## Consequences

- Command and UI packages receive ready services instead of constructing dependencies.
- Runtime wiring is easier to inspect and test from one place.
- New subsystems should be wired through bootstrap unless they are purely local to one package.
- Bootstrap can grow dense; keep construction readable and push implementation detail into subsystem constructors.
