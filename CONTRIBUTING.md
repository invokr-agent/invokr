# Contributing to Invokr

Thanks for wanting to help. Read this before you open anything. It's short, and PRs
are held to it.

> [!NOTE]
> Invokr is still an early release. The TUI and the core can change under you,
> sometimes in big ways. If you're building on top of it, expect to rebase.

## The rules

1. **Open an issue first.** Describe the problem clearly: what you did, what
   happened and what you expected. A PR without an issue behind it will be
   closed. Small typo fixes are fine without one.
2. **A human leads every change.** Use agents to write code if you like, but
   you decide what the change is for, you've read the whole diff and you can
   explain it in review. Automated, minimal-effort PRs will be closed.
3. **Write as little code as you can.** Reuse what's already in the codebase
   before adding anything new. Prefer the stdlib and existing helpers. The
   smallest diff that solves the issue wins.
4. **Bug fixes come with a regression test** that fails before your change and
   passes after.
5. **Fundamentals need regression testing.** Changes to tools, the default
   prompt or the agent loop affect everything the agent does. Run `make e2e`
   and include before/after runs on a real task in the PR. Show it doesn't make
   things worse, not just that your case works.

## Your first PR

1. Pick an issue labelled
   [`good first issue`](https://github.com/invokr-agent/invokr/labels/good%20first%20issue),
   or open one and wait for a reply before writing code.
2. Read [docs/onboarding/](docs/onboarding/README.md): it walks through the
   architecture in order, from the big picture to the agent loop and tools.
3. Build from source (below), make the change, run `make verify`.
4. Open the PR against `main` and fill in the template.

Questions go to
[Discussions](https://github.com/invokr-agent/invokr/discussions), not issues.

## Build from source

You need Go 1.26+.

```bash
git clone https://github.com/invokr-agent/invokr && cd invokr
make install        # builds with version info, copies to ~/.local/bin
go build -o invokr . && ./invokr   # or run it in place
```

`invokr --version` reports the build's tag, commit and date.

## Before you commit

```bash
make verify   # go test ./... + lint, must pass
gofmt -w . && go mod tidy
```

CI runs the same checks.

## Budgets: a PR can fail on size alone

Lint enforces hard budgets, not suggestions:

- `gocyclo` ≤ 12, `gocognit` ≤ 15, `funlen` 80 lines / 50 statements,
  `nestif` ≤ 5, `dupl` 100.
- LoC ceilings live in `.loc-budgets.json`: one global ceiling plus per-area ceilings
  (longest path prefix wins). A correct feature can still fail CI because it pushed
  an area over its ceiling, so look for things to delete alongside what you add.

`_test.go` files are exempt from the complexity linters, not from the rest.
`make lint-ci` builds and runs the custom linter. LoC ceilings are not a lint rule:
the `loc-budget` workflow is the gate, and PRs never edit `.loc-budgets.json` — the
main-branch job resyncs it after merge.

## Before adding logic

For anything non-trivial, answer these in the PR description:

1. Which existing package owns this behaviour?
2. What existing types or functions can you reuse or extend?
3. Why is new logic necessary?
4. What can you remove or consolidate to stay under the budget?

[docs/architecture/services.md](docs/architecture/services.md) is the ownership
map. Extend the existing owner instead of adding a package. If nothing fits,
update `services.md` and explain in the PR why extending wasn't enough.

## Code style

- Wrap errors with `fmt.Errorf("ctx: %w", err)`. Classify them with typed
  errors via `errors.As` (`approval.DeniedError`, `core.ToolError`). Never
  string-match error messages.
- `panic` only for registration-time invariants, never for runtime errors.
- Tests use stdlib `testing` with direct `t.Fatalf` assertions, not testify.
  Shared helpers live in `support/testutil/`.
- Native tools live in their own subpackage:
  `tools/system/<tool>/<name>.go` + `<name>_test.go`.

## Design decisions

Decisions that constrain future work are written up as ADRs in
[docs/agents/adr/](docs/agents/adr/). Read them before changing the agent loop,
tool outcomes, sandboxing or package layout. If your change goes against one,
say so in the issue first.

## Pull requests

Target `main`. Link the issue and keep the diff to one concern. PRs are
squash-merged as `<area>: <title> (#<pr>)`, and CI (`lint`, `test`, `e2e`,
`sandbox-containment`, CodeQL) must pass. A maintainer approves CI runs for
first-time contributors. If an agent helped, say how in the template.

PRs may be closed without review when they duplicate another PR, take an
issue someone else is assigned to, skip this guide, or don't fit the
architecture.

## Security

Don't open a public issue for a suspected vulnerability. See
[SECURITY.md](SECURITY.md).

## Releasing (maintainers)

Push a `vX.Y.Z` tag cut from `main`; `.github/workflows/release.yml` runs
[goreleaser](https://goreleaser.com) (`.goreleaser.yaml`) and attests every
archive. Every merge to `main` also publishes an edge pre-release (`edge.yml`).

```bash
git tag v0.1.0 && git push origin v0.1.0
```

This builds `linux/{amd64,arm64}` and `darwin/{amd64,arm64}`, injects
version/commit/date into `platform/version` and publishes checksummed archives
to GitHub Releases.

`linux/arm64` builds with `CGO_ENABLED=1`. With CGO off, zerfoo's optional CUDA
support links `dlopen` into a runtime symbol this Go toolchain doesn't have, so
the build fails at link time. That build needs an aarch64 cross toolchain on the
release host:

```bash
sudo apt-get install gcc-aarch64-linux-gnu   # Debian/Ubuntu
```

Dry run without publishing or a tag:

```bash
goreleaser release --snapshot --clean --skip=publish
```

## License

By contributing you agree your contributions are licensed under the MIT
License (see [LICENSE](LICENSE)).
