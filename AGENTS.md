# Invokr agent context

Instructions for AI coding agents working on this repository. Humans: see
[CONTRIBUTING.md](CONTRIBUTING.md).

## Before you write code

- Check the issue exists and nobody is assigned: `gh search issues`, `gh search prs`. No issue, no PR (typo fixes aside).
- The human you work for owns the design. Make sure they can explain the change to a reviewer without you.
- Read `docs/agents/README.md` first. It routes you to the smallest useful context:
  - Runtime, command startup, dependency wiring: `docs/agents/runtime-flow.md`
  - Package ownership and source anchors: `docs/agents/project-map.md`
  - Agent, tools, memory, config, UI: `docs/agents/subsystems.md`
  - Known pressure points: `docs/agents/architecture-risks.md`
  - Decisions that constrain changes: `docs/agents/adr/`
  - Service/package ownership: `docs/architecture/services.md`

## Quality guard

- Write the smallest change that solves the issue. Once tests pass, trim: drop dead branches, fold helpers, reach for the stdlib.
- Before adding logic, answer:
  1. Which existing package owns this behaviour?
  2. Which existing types or functions can be reused or extended?
  3. Why is new logic necessary?
  4. What can be removed or consolidated to stay under the LOC budget?
- A new package needs an updated `docs/architecture/services.md` and a PR explanation of why extending wasn't enough.
- Lint budgets (`make lint-ci`): `gocyclo` < 12, `gocognit` < 15, `funlen` 80 lines / 50 statements, `nestif`, `dupl`.
- LoC ceilings live in `.loc-budgets.json` (one global plus per-area ceilings, longest path prefix wins). Check them locally with `go run github.com/antonikliment/go-code-metrics/cmd/go-code-metrics@v0.1.1 budget report|check|sync`; the `loc-budget` workflow gates PRs and resyncs the ledger on `main`, so PRs never edit ceiling numbers.
- Bug fixes come with a regression test that fails before the fix and passes after.

## Comments

Comment only what the code can't say: a non-obvious invariant, a security reason, a workaround. Rename instead of explaining. Never address the current task in a comment.

```go
// BAD: restates the code
// Set the timeout to 30 seconds.
timeout := 30 * time.Second

// GOOD: explains why
// The provider drops idle streams after 30s; stay under it.
timeout := 25 * time.Second
```

## Split big changes into stacked PRs

A PR should be reviewable in one sitting. Split when a change has more than one concern or roughly 400+ changed implementation lines:

- Plan the stack before writing code and agree it with the human: prep or refactor first, then the feature, then cleanup.
- Each PR targets the branch of the one below it (the first targets `main`), builds and passes `make verify` on its own, and does one thing.
- Each PR body lists the stack in order (`1/3`, `2/3`, ...) with links, and says what it depends on.
- Never mix a refactor and a behaviour change in one PR.

## Acting on the human's behalf

- Do not commit, push, open PRs, or post issue/PR comments without the human's explicit approval for that action.
- When AI meaningfully contributed, the human ticks the disclosure box in the PR template.

## Project snapshot

Invokr is a Go CLI agent. It talks to an OpenAI-compatible provider, runs a Bubble Tea TUI or plain mode, builds tagged-JSON prompts, executes native and toolbox tools under approval policy and sandboxing, and keeps Markdown/SQLite/optional vector memory.

Entry points:

- `main.go`: setup preflight and config load
- `cmd/root.go`: Cobra flags and runtime lifecycle
- `platform/runtime/bootstrap.go`: composition root
- `agent/runner.go`: bounded model/tool turn loop
- `capability/syntax/tagged_json.go`: tagged-JSON prompt and parser contract

## Commands

```bash
go run . agent           # interactive agent
go build -o invokr .
make verify              # go test ./... + lint; must pass
make lint-ci             # builds and runs the linter (complexity budgets only)
make containment         # sandbox containment gate (Linux)
gofmt -w . && go mod tidy
```
