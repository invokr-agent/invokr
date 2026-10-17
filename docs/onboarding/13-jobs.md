# 13 — Scheduled Jobs

Packages: `capability/jobs/` (store + crontab), `capability/jobs/jobrun/`
(execution), `tools/system/schedule/` (agent-facing tool), `cmd/jobs_cli.go`.

## Model

A job = name + 5-field cron schedule + prompt + approval policy, stored in the
shared `memory.db` (`jobs` table). Scheduling is delegated to the **OS
crontab**: `jobs.Install` writes a two-line block

```cron
# invokr-job:<id>
<schedule> '<abs path to binary>' jobs run <id>
```

idempotently (marker comment is the key); `Strip`/`StripAll` remove blocks
while preserving the rest of the crontab byte-for-byte. Installing from a
temp-dir binary earns a warning (the entry would break when the binary
vanishes).

`SanitizeSchedule` validates structure only (5 fields, cron charset, numeric
ranges per field) — it doesn't parse semantics.

## Execution

`invokr jobs run <id-or-name>` (what cron invokes) → `jobrun.RunJob`:
bootstraps a **fresh runtime**, wires a goroutine that answers every approval
per the job's policy (`allow`/`deny`), sends the prompt, records
`last_run_at`/`last_status` (status strings truncated to 255 runes), 30-minute
timeout. `jobrun` is a separate package purely to break the import cycle
(`runtime` → `tools/system` → `schedule` → `jobs`).

Inside a live TUI, `/jobs run <name>` runs the same job on a fresh chat
session via `CommandService.runJob` (guarded against double-starts, completion
reported as a notice).

## Three front ends, one store

| Front end | Can set policy? |
|---|---|
| `invokr jobs add --schedule … --prompt … [--allow <tier>] [--allow-all]` | yes — `--allow` stores a comma list of tiers matched like `exec --allow`; `--allow-all` ⇒ `allow` (with a loud warning) |
| `schedule` tool (agent) | no — always `deny`; every action itself approval-gated; add/modify/remove roll back the DB on crontab failure |
| `/jobs` in the TUI | run/list only |

The `deny` policy means an unattended job that hits an approval-gated tool
fails — the `schedule` tool's description warns the model about this.

The agent-facing tool distinguishes registration from execution: add/modify
confirm the saved cron expression, while list shows `unverified (no recorded run)`
until a run status exists. Registration alone does not establish cron daemon
availability. Setup verification is bounded by tool guidance: run the underlying
command/probe once with normal approvals, list the saved job once, and report
scheduled execution as unverified if no run was recorded. Keep the requested job;
do not wait for cron ticks, poll repeatedly, or create marker jobs. Scheduler
investigation requires an explicit user request. This guidance is not a runtime
quota on shell or schedule calls.

## Gotchas

- `jobs` CLI subcommands skip the root runtime bootstrap
  (`skipsRuntimeBootstrap`) — they only open the DB; `jobs run` bootstraps its
  own runtime inside `RunJob`. Don't add code there that assumes runtime
  services exist.
- `--fail-on-tool-error` makes a run with any tool failure exit non-zero and
  records `error: N tool failure(s)`.
- Job IDs are UUIDs; `rm`/`run` accept names too (`GetByName`).
