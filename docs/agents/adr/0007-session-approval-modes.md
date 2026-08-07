# ADR 0007: Session approval modes

Status: accepted

## Decision

Issue #489 replaces `default` and `read-only` with Manual, Auto, and Plan.
Shift+Tab cycles these modes; `/mode manual|auto|plan` selects one directly.
`/approvals` remains an alias (`default` means Manual). Startup selection is
`--mode manual|auto|plan`, supported by the TUI and `exec` only.

| Mode | Behavior |
| --- | --- |
| Manual | Existing configured tool approvals and standing rules. |
| Auto | Native workspace edits, native reads, and demonstrably confined execution run without prompting. Network and local Git commits are allowed. Unconfined execution and protected edits require approval. |
| Plan | Inspection, clarification, and planning. Shell/Python verification requires read-only sources with separate writable scratch/cache. Network asks; source writes and recognized publishing are denied. |
| YOLO | CLI-only `--yolo`; skips Invokr approval, namespace, Docker relaunch, and workspace restrictions. Explicit standing denials still apply. Fixed until restart, with a pinned TUI warning and a startup stderr warning. |

Modes and publishing permission are process-local, never restored from a saved
session. CLI mode flags cannot be combined with legacy `exec --allow` or
`--allow-all`; the latter retains its previous meaning and is not YOLO.

New calls take the current mode. Already-running calls and persistent processes
keep their original execution boundaries; the TUI reports work from an earlier
mode. Pending approvals remain visible until the user decides. Before executing
an approved request, recheck current policy: switching to Plan can still forbid a
source write, while an unrelated publishing toggle does not discard approval.

`--allow-publishing` or `/publishing allow` authorizes recognized publishing in
Auto for the session; `/publishing ask` restores per-call approval. The existing
shell AST recognizes common Git, GitHub CLI, registry publishing commands and
literal shell/wrapper forms. Arbitrary network code can publish undetected:
this is a command-shape guard, not a network exfiltration guarantee. Approval
does not inject host credentials into a sandbox.

## Consequences

`capability/approval.Policy` owns decisions. The shared caller establishes policy
context even for automatic tools; execution owners supply prepared facts through
`core.EnforceApproval`. Standing denials are checked before every mode decision.
Dynamic metadata cannot prove read-only execution or confinement. Delegation is
automatically allowed because child tools use the same live policy; toolbox,
MCP, and LSP execution are unavailable in Plan.

Invokr configuration/grants, workspace `.invokr`, and Git config/hooks are
protected. Native edits require one-call approval in Auto, never a persistent
grant. Existing shell/plugin protected mounts stay read-only. Bind mounts cannot
protect absent names without changing the host or making their parent read-only.

Locked D1 rule: foreground shell and process-backed `code_interpreter` calls in
Auto may run with absent protected names unsealed. Before starting, snapshot
those names with `Lstat`. After the process exits, remove any that appeared and
fail the call even if it exited zero, naming removed paths. Existing protected
paths remain read-only and are never cleanup candidates. Cleanup uses an opened
filesystem root: a newly created symlink is removed, not followed outside it.
Python keeps its read-only source mounts; this rule does not grant workspace
writes or bypass approval for copying an artifact to a protected destination.

In a non-git workspace, root `git init` creates the protected `.git` name;
post-run cleanup removes it and fails the call. Auto therefore cannot initialize
a repository at the workspace root. Initialize it in Manual mode or your terminal
before switching to Auto.

Background shell, tmux, toolbox (including daemons), and other process launches
still require one-call approval for missing protected names. That approval covers
the process and its children, never a standing grant. If approved execution for
the same writable root overlaps a foreground snapshot, preserve any newly
appearing protected name and fail the foreground call with a conflict message.
Bookkeeping retains the approval generation even after that process exits, so
its completed creation is not deleted by a slower foreground call. Coordination
is within one Invokr process; separate Invokr instances and external filesystem
writers do not share this bookkeeping.

This is post-run detection, not prevention: a hook created and executed within
the same shell line can run inside the sandbox before cleanup. A name created
and removed before the check is not detected. The accepted tradeoff allows
ordinary unattended Auto shell work without creating host control directories
at setup or making the workspace read-only. No failed sandbox launch falls back
silently to the host. Native protected edits still require explicit approval.

An unreadable standing-rule store preserves Manual's existing fallback, but
blocks Auto/Plan/YOLO tool execution until repaired; switching back to Manual is
allowed. Non-interactive Auto/Plan deny requests that still need a person, so
publishing in Auto `exec` needs `--allow-publishing`. Plan never changes mode itself.
