# 15 — Testing

Packages: `support/testutil/` (+ `providertest/`, `clitest/`), `e2e/`
(build-tagged), `cmd/e2ereport/`. ADR 0004 (oracle-based testing),
`docs/glossary.md` for the vocabulary.

## Style

- Stdlib `testing`, direct `t.Fatalf` — **no testify**.
- `_test.go` files are exempt from the complexity linters, so tests can be
  long and explicit.
- Assert on **oracles**: exit codes, file hashes, sqlite rows, JSONL ledger
  events, provider request snapshots. Never on model prose (ADR 0001/0004).
- `make verify` = `go test ./...` + lint. CI runs tests verbose plus
  golangci-lint on PRs to `main`.

## The fake provider (`support/testutil/providertest`)

An in-process `httptest` server that scripts the conversation step by step:

```go
srv := providertest.NewScripted(t,
    providertest.NativeToolCallStep("call-1", "shell", map[string]any{"command": "ls", "args": []string{}}),
    providertest.ReplyStep("done"),
)
defer srv.Close()
// point the client/bootstrap at srv.URL()
```

- Steps: `ReplyStep` (tagged-JSON reply), `ToolCallStep` (tagged syntax),
  `NativeToolCallStep`, `SSEReplyStep`, `ErrorStep(status, body)`.
- Fault fields on any step: `MalformedBody`, `DropAfterChunks`,
  `StallAfterChunks`+`StallDuration`, `RetryAfterSeconds`, plus hooks
  (`BeforeResponse`, `MutateForRequest`, `AfterChunk`).
- Also serves `/props` (context window), `/v1/embeddings` (canned vectors),
  and 404s `/v1/models` so model fallback stays inert.
- Assertions: `Snapshot()` (all requests), `AssertClean` (no protocol
  surprises), `AssertExhausted` (every step consumed), `MessagesContain`.

## CLI-level tests (`support/testutil/clitest`)

Builds the real binary once per test run (`Binary(t)`), then runs it with a
fully isolated environment (`NewState`: temp HOME/toolbox/memory/work dirs,
tokf disabled, `NO_COLOR`, dumb TERM). Use for `exec`-shaped end-to-end
behavior inside normal `go test`. Processes get their own process group so
timeouts kill the whole tree.

## E2E suite (`e2e/`, `-tags e2e`)

Two tiers (glossary has the full vocabulary):

- **Fake tier** — scripted provider, bit-deterministic, **gates** CI.
- **Live tier** — `INVOKR_E2E_LIVE=1`, drives a real local model; advisory
  (only a canary exfiltration fails it). Case IDs (`C..`/`D..`/`R..`/`A..`)
  map to the manual test plan; `cmd/e2ereport` turns `go test -json` output
  into the markdown report and understands the skip-prefix states
  (`BLOCKED:`, `SELECTION:`, `KNOWN:`).

## SWE task cases (`e2e/swe_test.go`, `S..`)

Where `C../D../R../A..` ask "did the agent route correctly", the `S..` cases ask
the SWE-bench question: given a repo with a failing test, does the agent produce
a patch that makes the repo's own suite pass. `make e2e-swe` runs the scripted
tier (no model, `INVOKR_E2E_LIVE` cleared); `make e2e-swe-live` runs the live
tier. Each target's `-run` regex is anchored to its own tier, so a case must be
named `TestS<NN>_` or `TestLiveS<NN>_` to be picked up.

- Fixture: a small Python package (`textstats`) with a case-sensitivity bug,
  committed to a throwaway git repo in the workspace. Needs `python3` and `git`;
  reports `BLOCKED:` where either is missing.
- Three oracles, all machine-checkable state: a diff against the base commit
  exists, FAIL_TO_PASS now exits 0, PASS_TO_PASS still exits 0. Plus a `tests/`
  tamper check, since a rewritten suite passes PASS_TO_PASS trivially.
- `TestS01` scripts the patch through the fake provider and **gates** the run;
  `TestS02` is the negative control (an untouched repo must be reported as a
  miss); `TestLiveS01` drives a real model and is **advisory only** — an unsolved
  task logs `ADVISORY[S01] KNOWN: …` and skips, so a weak model never reds the
  build. Only harness breakage fails the live case.
- Model: the live SWE case uses `INVOKR_E2E_SWE_MODEL` if set, else
  `INVOKR_E2E_LIVE_MODEL`, and with neither set it skips `BLOCKED:` rather than
  scoring an unnamed default — a SWE score is a tracked metric, so it must be
  attributable to a model somebody chose. Raise the score by pointing the run at
  a stronger model, not by editing the case. `INVOKR_E2E_SWE_TIMEOUT` (default
  `10m`) bounds one attempt.

## Handy patterns seen in the tree

- `zerfoo.NewWorkerWithLoaders` — inject fake embed/gen models; never load
  GGUFs in unit tests.
- Model-backed integration tests download the default GGUFs into the gitignored
  `.modelcache/` (see `support/testutil.UseRepoModelCache`).
- Toolbox tests write plugins with `testutil.MustWriteToolboxPlugin`.
- Time-sensitive code takes a `now func() time.Time` (memory stores,
  retrieval, web cache) — override it rather than sleeping; delegate timeouts
  take a `timeoutUnit` for the same reason.

## What to test when you change X

| Change | Minimum coverage |
|---|---|
| A tool | Decode/Prepare validation, approval decision branches, Execute against a temp workspace, audit payload |
| Agent loop behavior | providertest script driving the exact turn sequence + ledger assertions |
| Config knob | defaults + file + env paths |
| Memory | sqlite store round-trip (`:memory:` works via `InitDbAtPath`) + curator/event flow |
| Anything concurrent | run with `-race` locally; the TUI queue and caller registry are the usual suspects |
