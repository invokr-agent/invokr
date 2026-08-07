.PHONY: dev record generate test containment e2e e2e-fixture e2e-swe e2e-swe-live e2e-docker e2e-live e2e-report lint lint-ci verify analyze diagram-view install

INSTALL_DIR ?= $(HOME)/.local/bin

CUSTOM_LINT ?= ./custom-golangci-lint

# Built from tools/customlint (a separate module) with plain `go build`, so it
# resolves through GOPROXY / the module cache. `golangci-lint custom` is not
# used: it shells out to `git clone` of the golangci-lint repo, which fails in
# CI containers without GitHub access.
custom-golangci-lint: tools/customlint/main.go tools/customlint/go.mod tools/customlint/go.sum
	go build -C tools/customlint -o "$(CURDIR)/custom-golangci-lint" .

test:
	go test ./...

containment:
	python3 -m unittest discover -s scripts -p test_containment.py
	python3 scripts/containment.py

e2e:
	go test -tags e2e -timeout 20m ./e2e/...

# Force the scripted fixture (no model) tier regardless of ambient env: even if
# INVOKR_E2E_LIVE=1 is exported in the shell, live cases skip here.
e2e-fixture:
	INVOKR_E2E_LIVE= go test -tags e2e -timeout 20m ./e2e/...

# SWE task cases (S..) split by tier, and each -run regex is anchored so it
# matches only its own tier: TestS<NN>_ is scripted, TestLiveS<NN>_ is live. An
# unanchored 'S[0-9]+_' would match both, so a case named outside this
# convention silently lands in the wrong target (or in neither).
#
# e2e-swe: scripted cases only. INVOKR_E2E_LIVE is cleared so the tier never
# depends on ambient env, matching e2e-fixture.
e2e-swe:
	INVOKR_E2E_LIVE= go test -tags e2e -timeout 40m -v ./e2e/... -run '^TestS[0-9]+_'

# e2e-swe-live: the live cases. Needs a provider at INVOKR_E2E_LIVE_URL and a
# model named by INVOKR_E2E_SWE_MODEL or INVOKR_E2E_LIVE_MODEL; with neither
# set the live case skips rather than scoring a model nobody chose.
e2e-swe-live:
	INVOKR_E2E_LIVE=1 go test -tags e2e -timeout 40m -v ./e2e/... -run '^TestLiveS[0-9]+_'

e2e-docker:
	docker compose -f compose.e2e.yml run --rm e2e

e2e-live:
	docker compose -f compose.e2e.yml --profile live run --rm e2e-live

e2e-report:
	go test -tags e2e -json -timeout 20m ./e2e/... | go run ./cmd/e2ereport -out docs/private/manual-test/e2e-$$(date +%F).md

lint:
	@if [ -x $(CUSTOM_LINT) ]; then \
		$(CUSTOM_LINT) run; \
	else \
		golangci-lint run ./...; \
	fi

lint-ci: custom-golangci-lint
	$(CUSTOM_LINT) run

verify: test lint

# install builds invokr-agent with version info baked in via -ldflags and
# copies it to INSTALL_DIR (default ~/.local/bin).
install:
	go build -ldflags "-X github.com/antonikliment/invokr/platform/version.Version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev) -X github.com/antonikliment/invokr/platform/version.Commit=$$(git rev-parse --short HEAD 2>/dev/null || echo none) -X github.com/antonikliment/invokr/platform/version.Date=$$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o invokr-agent .
	mkdir -p $(INSTALL_DIR)
	cp invokr-agent $(INSTALL_DIR)/invokr-agent
	@echo "installed $(INSTALL_DIR)/invokr-agent"

# dev builds from source and installs it as `invokr-dev` (INSTALL_DIR, default
# ~/.local/bin) with the commit and build date stamped into the TUI banner.
dev:
	./scripts/build-dev.sh

# record re-records the README demo GIF. Needs uv, a model server the agent can
# reach, and (optionally) ImageMagick to shrink the result.
record:
	./docs/assets/record/record.sh

# analyze prints a size/complexity breakdown and writes an interactive treemap.
analyze:
	go tool sizeanalyzer -html size-report.html

# diagram-view renders docs/onboarding/16-diagrams.md's mermaid diagrams as a
# single HTML page and opens it in a browser.
diagram-view:
	./scripts/diagram-view.sh
