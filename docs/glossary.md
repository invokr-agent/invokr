# Glossary

Ubiquitous terms for Invokr. Add a term when a design decision fixes its meaning; link cases and
ADRs.

## Testing

- **Oracle** — the machine-checkable signal a test asserts on: an `exec --json` ledger event, a
  file hash, a sqlite query result, an exit code, a log grep, or a provider request snapshot. Model
  prose is never an oracle (ADR 0001, ADR 0004).
- **Fake tier** — the scripted-provider tier of the E2E suite. Bit-deterministic, no GPU, **gates**
  the run (a failure fails CI). See ADR 0004 and `e2e/`.
- **Live tier** — the opt-in tier that drives a real `ornith-9b` via a Docker Compose CUDA service
  (`INVOKR_E2E_LIVE=1`). **Advisory**: never fails the run — except on a real canary exfiltration.
- **Gate vs advisory** — a *gating* check can fail the run; an *advisory* check only records an
  observation (`t.Log`, report note). The fake tier gates; the live tier is advisory.
- **Fault step** — an explicit `providertest.CompletionStep` field that injects a provider fault
  deliberately: `RetryAfterSeconds`, `DropAfterChunks`, `MalformedBody`, `StallAfterChunks` +
  `StallDuration`. Replaces the manual R-suite's Python `fault_provider.py`.
- **Stall watchdog** — `clients.Client.StallTimeout`: bounds the wait for the first stream chunk
  (across SDK retries) and every inter-chunk gap, surfacing `provider stalled: no stream data for
  <d>` instead of hanging. Fixes the R02c hang.
- **Manual-later** — a case documented as non-automatable (prose quality, visual TUI fidelity,
  real-model resistance/selection). Listed once in the suite spec's appendix; a manual run must
  still cover it.

## Result states

The e2e report generator maps `t.Skip` prefixes to
these:

- **PASS / FAIL** — runtime behavior and external state match / violate the expectation.
- **BLOCKED** — a required environment dependency (e.g. Linux user namespaces) is unavailable; Invokr
  is not blamed. `t.Skip("BLOCKED: ...")`.
- **SELECTION** — a natural prompt did not select the intended tool (live tier); advisory, never
  gates.
- **KNOWN** — reproduces a documented limitation without a new regression.
- **INJECTION-FOLLOWED** — the model obeyed an injected instruction but a runtime guard stopped the
  harmful effect. An observation, not a FAIL (A-suite; ADR 0004).
