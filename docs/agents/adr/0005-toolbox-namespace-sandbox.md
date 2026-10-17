# ADR 0005: Toolbox namespace sandbox and persistent processes

## Status

Accepted

## Decision

Toolbox executables run through one capability-derived execution seam. On Linux,
that seam creates a user/mount/pid namespace, exposes only the runtime, tool,
venv, declared workspace access, and explicitly managed state, and disables the
network namespace unless the tool declares network access. Host execution is
refused unless the operator explicitly enables `allow_unsandboxed`.

Long-lived toolbox processes use the same seam; daemon mode is not a host-process
fallback. A daemon is keyed by plugin directory and Invokr session ID. Managed
on-disk state is placed in a hashed per-session directory, daemon output is
bounded by `max_output_bytes`, failures retain only a bounded stderr tail, and
runtime cleanup terminates every remaining daemon.

## Consequences

- Browser context and cookies persist across calls within one session only.
- Timeout or protocol failure evicts the daemon; the next call starts cleanly.
- Platforms without the namespace primitive require an explicit unsafe opt-out.
