# bench — harness + model benchmark

A small deterministic suite that measures the Invokr harness and the bare
model side by side against a live OpenAI-compatible provider. No LLM judge:
scoring is BFCL-style AST matching or the task's own tests.

```
git clone git@github.com:antonikliment/test-bench.git bench/test-bench
go build -o invokr-bench ./bench
./invokr-bench -tasks bench/test-bench/tasks -model <id> [-url http://localhost:8080] [-tiers toolcall,codeedit,terminal] [-baseline=true]
```

Tasks are **not** vendored here — they live in
[antonikliment/test-bench](https://github.com/antonikliment/test-bench) along with
`fetch_tasks.py`, which regenerates them from upstream. `bench/tasks/`,
`bench/test-bench/` and `bench/fetch_tasks.py` are gitignored so bench runs never
leak task code into unrelated PRs.

Each task runs twice: through `invokr exec --json --allow-all` in an isolated
temp home/workspace (with `INVOKR_AGENT_RUN_TIMEOUT` set to 70% of the per-run
timeout so the harness wraps up before the hard kill; set `INVOKR_TOOLS=minimal`
in the environment to advertise only the small-model tool set), and (where meaningful) as one bare chat completion to the
same provider. The delta attributes outcomes to the harness vs the model.

## Tiers

| tier | tasks | source | scoring |
|---|---|---|---|
| toolcall | 10 | BFCL v3 simple split (Apache-2.0) | expected function called, args in allowed sets |
| codeedit | 12 | nuprl/CanItEdit (8) + Aider polyglot python (4) | task's own test suite |
| terminal | 3 | terminal-bench-1 easy tasks | task's pytest oracle |

The terminal tasks keep their upstream canary GUIDs. `bench/` is excluded from the LOC budget
(`.golangci.yml`, `budget_test.go`) like `cmd/e2ereport`.

Results land in `bench-results.json` (gitignored); tracked findings go in
`docs/research/bench-report.md`.
