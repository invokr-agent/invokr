# Security Policy

## Supported versions

Invokr has no long-term support branches. Only the latest commit on `main`
receives security fixes. If you are running an older revision, update before
reporting.

## Reporting a vulnerability

Report privately through GitHub's private vulnerability reporting:
<https://github.com/antonikliment/invokr/security/advisories/new>.

Please do not open a public issue for a suspected vulnerability.

Include a description, affected version/commit, and reproduction steps.
Expect an acknowledgement within 7 days and a status update within 30 days.

## Scope

Invokr runs LLM-directed code and shell commands on your machine. Some of it
is deliberately *not* isolated. The following are **known, documented
non-boundaries** and are not accepted as vulnerabilities:

- **`code_interpreter`'s unsandboxed process backend is not a security
  boundary.** Invokr prefers a container engine when one is available. The
  process fallback runs Python in the namespace sandbox when it is available;
  without it the tool refuses unless `INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1` is
  set, and then Python runs on the host with your privileges. `--yolo` skips
  the sandbox and runs on the host. The source-token checks are advisory only
  and trivially bypassable. See `tools/system/code_interpreter/`.
- **Unsandboxed toolbox execution is an explicit opt-out.** Toolbox tools
  refuse to run when namespace sandboxing is unavailable unless the user sets
  `INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1`. Setting it means tools run on the host
  with no isolation; that is the documented consequence, not a bug. See
  `tools/system/toolbox/`.
- **Approval gating is a user-consent mechanism, not a containment mechanism.**
  Once you approve an action, it runs with your privileges.
- **Provider-process PATH fallback is a documented migration concession.**
  Provider binaries installed via `invokr provider install` are verified
  against an Ed25519-signed registry, but an unsigned binary named
  `go-inference-router-provider-*` on PATH still runs when no verified install
  exists for that type. This is deliberate for one release (see
  `clients/provider_install.go`); reports that an attacker who can already
  write to your PATH can plant such a binary are out of scope. A bypass of the
  registry signature or digest verification itself is **in** scope.
- **The model itself is untrusted input.** Prompt injection that causes the
  model to *request* a dangerous action is expected; the defence is the
  approval gate and the sandbox, not the model. Reports amounting to "the model
  can be persuaded to ask for something bad" are out of scope. Reports where a
  requested action bypasses the approval gate or escapes a sandbox that was
  supposed to hold are **in** scope.

In scope: approval-gate bypass, sandbox escape from the container/namespace
backends, leakage of credentials or session data to unintended destinations,
verification bypass in the signed provider-binary install path, and any path
that executes code without a gate that should have applied.

The mechanics — tiers, sandboxes, standing grants, and every default named
above — are documented in [docs/security-model.md](docs/security-model.md).
