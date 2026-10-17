# 07 — Approvals

Package: `capability/approval/`. agents/adr 0007 (session approval modes).

## The broker

`Broker` is an unbuffered channel rendezvous: a tool calls
`RequestApproval(ctx, Request)` and blocks; a frontend receives the `Pending`
from `broker.Requests()` and calls `Decide(Decision)`. A denial comes back as
the typed `*approval.DeniedError` (classify with `errors.As`, never by
message). Context cancellation unblocks the requester.

`Request` carries `ID` (uuid), `Tool`, `Reason` (why approval is needed),
`Tier` (policy tier string), `Summary`, `Details` (multi-line, rendered in the
expanded TUI panel / chat prompt).

## How each frontend answers

| Frontend | Mechanism |
|---|---|
| TUI | `ApprovalQueue` drains remaining asks into a visible batch; keys `a/d/A/D/v`; ctrl+c denies all. Shift+Tab or `/mode` chooses Manual/Auto/Plan. Policy runs before the queue, even for automatic tools. |
| `exec` | `--mode auto|plan` uses the same policy and denies remaining asks. Legacy Manual flags remain `--allow <tier>` (repeatable) and `--allow-all`; do not combine them with mode flags. |
| Jobs | Fixed per-job policy `allow`/`deny` recorded at creation; a goroutine drains and answers uniformly. Jobs created by the agent-facing `schedule` tool are always `deny`. |
| Chat | The transport asks in-channel: Telegram inline buttons, Signal `YES <id>`/`NO <id>` replies; 2-minute timeout denies. |

The queue fails closed: enqueue after `Close()` denies immediately (never
strands a blocked requester), and `Decide`/`DecideAll` are idempotent.

## Where approval decisions are made

Tools declare an `Approve` func returning `core.Auto()`, `core.Ask(request)`,
or `core.Deny(reason)`; `core.EnforceApproval` runs it before `Execute`.
Missing approver + Ask = error (fail closed). Examples of *conditional* asks:
shell tiers, apply_patch dry runs only for hidden/sensitive paths (a real apply
always asks), web_search only for sensitive queries, web_fetch always (web_network),
code_interpreter always,
delegate_task always.

## Denial memory (per request)

`TurnProcessor` hashes each call (`core.CallHash` = name + canonicalized JSON
args). After a user denies a call, an identical retry in the same request is
refused without re-prompting (`DeniedRetryPayload`), and the payload tells the
model the denial is final. `ResetCallHistory` clears this when a new user
message arrives — a new message can change intent.

## Adding approval to a new tool

1. Pick/declare a tier string (see `constants/` for existing ones).
2. Set `Metadata` via the helpers (`WriteWorkspaceAskMetadata`, …) so
   policy/capabilities are visible to `tool_search`; session modes also require
   execution owners to supply accurate prepared facts to `core.EnforceApproval`.
3. Implement `Approve` — return `core.Auto()` for the safe cases so users
   aren't drowned in prompts (see the `memory` tool's rationale).
4. Write a `Summary` a human can act on in two seconds, and put the scary
   details (full command, diff, package list) in `Details`.
