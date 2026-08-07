# Contributing to Invokr

Invokr is a Go CLI agent (module `github.com/antonikliment/invokr`, Go 1.26). This is the
human-facing summary; `AGENTS.md`, `CLAUDE.md`, and `docs/agents/README.md`
carry the full detail and are the source of truth if anything here disagrees.

## Before you commit

```
make verify   # go test ./... + lint — must pass
```

CI runs the same checks. Also run `gofmt -w . && go mod tidy`.

## Budgets — a PR can fail on size alone

Lint enforces hard budgets, not suggestions:

- `gocyclo` ≤ 12, `gocognit` ≤ 15, `funlen` 80 lines / 50 statements,
  `nestif` ≤ 5, `dupl` 100.
- `goclocbudget`: total implementation Go is capped. A correct, well-written
  feature can still fail CI because it pushed the repo over the line. Budget
  for deletions alongside additions.

`_test.go` files are exempt from the complexity linters, not from the rest.

Use `make lint-ci` to build and run the custom linter (the stock
`golangci-lint` binary does not enforce `goclocbudget`).

## Before adding logic, answer these four questions

Put the answers in the PR description when the change is non-trivial:

1. Which existing package owns this behaviour?
2. Which existing types or functions can be reused or extended?
3. Why is new logic necessary?
4. What can be removed or consolidated to stay under the LOC budget?

## Package ownership

`docs/architecture/services.md` is the ownership map. Extend the existing owner
rather than adding a package. A new package is allowed only when no existing
owner fits — and then you must update `services.md` and explain in the PR why
extension was not enough.

## Code style

- Wrap errors: `fmt.Errorf("ctx: %w", err)`. Classify with typed errors via
  `errors.As` (e.g. `approval.DeniedError`, `core.ToolError`) — never
  string-match error messages.
- `panic` only for registration-time invariants, never for runtime errors.
- Tests use stdlib `testing` with direct `t.Fatalf` assertions. testify is not
  the house style. Shared helpers live in `support/testutil/`.
- Native tools live in their own subpackage:
  `tools/system/<tool>/<name>.go` + `<name>_test.go`.

## Architecture decisions

Decisions that constrain future work are recorded as ADRs in `docs/adr/`.
Read the existing ones before changing agent-loop, tool-outcome, sandbox, or
package-layout behaviour. If your change contradicts an ADR, add a new ADR
superseding it rather than quietly diverging.

## Releasing

Tag and run [goreleaser](https://goreleaser.com) (`.goreleaser.yaml`):

```bash
git tag v0.1.0 && git push origin v0.1.0
goreleaser release
```

This builds `linux/{amd64,arm64}` and `darwin/{amd64,arm64}`, injects
version/commit/date into `platform/version` via `-ldflags -X`, and publishes
checksummed archives to GitHub Releases. `invokr-agent --version` reports
what was injected.

`modernc.org/sqlite` is pure Go, so three of the four targets cross-compile
CGO-free. The exception is `linux/arm64`: `github.com/zerfoo/zerfoo`'s
optional CUDA support resolves `dlopen`/`dlsym` via a `//go:linkname` into
`runtime.dlopen` on that target when `CGO_ENABLED=0`, and that runtime symbol
doesn't exist in this Go toolchain — a link-time failure, not a runtime one
(Invokr never touches CUDA). `.goreleaser.yaml` builds `linux/arm64` with
`CGO_ENABLED=1` instead, which routes zerfoo through its real cgo `dlopen`
path. That build needs an aarch64 cross toolchain on the release host:

```bash
sudo apt-get install gcc-aarch64-linux-gnu   # Debian/Ubuntu
```

Dry-run without publishing or requiring a tag:

```bash
goreleaser release --snapshot --clean --skip=publish
```

## Pull requests

Target `dev`, not `main`. Fill in `.github/pull_request_template.md`. Keep the
diff to one concern.

## Security

Do not open a public issue for a suspected vulnerability — see
[SECURITY.md](SECURITY.md).

## License

By contributing you agree your contributions are licensed under the MIT
License (see [LICENSE](LICENSE)).
