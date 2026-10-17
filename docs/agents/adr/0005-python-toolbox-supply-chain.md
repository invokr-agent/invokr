# ADR 0005: Lock Python Toolbox Dependencies Before Installation

## Status

Accepted

## Decision

Invokr treats every changed `requirements.txt` as untrusted supply-chain
input. Direct requirements must be exact registry pins. Before installation,
pip resolves the complete wheel-only dependency graph through a local
HTTP/CONNECT proxy that permits only configured index and artifact hosts.

Invokr rejects incomplete reports, non-allowlisted artifact URLs, missing
SHA-256 hashes, and releases newer than the configured cooldown (14 days by
default). The user must approve the resolved names, exact versions, ages, and
hashes. Invokr then persists `requirements.lock`, downloads verified wheels
through the restricted proxy, and installs from that local cache with
`--no-index --require-hashes`.

The environment remains per tool by default and preparation remains serialized
per venv. Existing tools migrate when their requirement marker is absent or no
longer matches.

## Consequences

- First use and changed requirements require network access and explicit
  approval, and may take longer than a normal tool call.
- Newly published releases are unavailable until the cooldown expires unless
  operators deliberately lower the configured value.
- Private indexes must configure their metadata format and every index and
  artifact host explicitly; redirects cannot expand network authority.
- Source distributions, VCS references, local paths, and unhashed artifacts
  fail closed.
