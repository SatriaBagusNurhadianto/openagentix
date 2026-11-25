# OpenAgentix: the same checks CI runs (.github/workflows/ci.yml), runnable locally. `make help` lists the targets.
# Go modules are discovered (hack/ci/go-modules.sh), so a new service needs no edit here.
SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

GO_MODULES    := $(shell hack/ci/go-modules.sh 2>/dev/null)
SHELL_FILES   := $(shell git ls-files '*.sh' 2>/dev/null)
GITLEAKS_ARGS ?=
# shellcheck is a report until the tree is clean: SHELLCHECK_STRICT=1 makes any finding fail `make lint`.
SHELLCHECK_STRICT ?= 0

# need <tool> [hint]: fail early with a clear message when a tool is missing.
define need
@command -v $(1) >/dev/null || { echo "ERROR: '$(1)' is required$(if $(2), ($(2)))." >&2; exit 1; }
endef

.PHONY: help test test-go test-chart test-shell test-integration test-console e2e lint lint-go lint-shell secrets cover

help: ## List the targets
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z_-]+:.*## /{printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

test: test-go test-chart test-shell ## Everything that runs offline (no Docker, no cluster)

test-go: ## go test -race over every Go module (COVER=1 writes coverage.out in each module)
	$(call need,go)
	@for m in $(GO_MODULES); do \
	  echo "=== go test $$m"; \
	  (cd $$m && go test ./... -race -count=1 $(if $(COVER),-coverprofile=coverage.out -covermode=atomic)); \
	done

cover: ## Per-module coverage summary
	@$(MAKE) --no-print-directory test-go COVER=1
	@for m in $(GO_MODULES); do printf '%-28s ' $$m; (cd $$m && go tool cover -func=coverage.out | tail -1); done

test-chart: ## Render tests over `helm template` plus helm lint (needs helm, jq, yq = the Python jq wrapper)
	$(call need,helm)
	$(call need,jq)
	$(call need,yq,the Python jq wrapper: pip install yq==3.4.3; NOT mikefarah/yq)
	@yq --version 2>&1 | grep -q '^yq 3\.' || { echo "ERROR: yq must be the Python wrapper (kislyuk/yq 3.x), found: $$(yq --version 2>&1 | head -1)" >&2; exit 1; }
	helm lint charts/openagentix
	charts/openagentix/tests/run.sh

test-shell: ## Offline shell and Python suites: channels, dashboard (jsdom part needs `npm ci` in website/), bench
	$(call need,jq)
	$(call need,yq,the Python jq wrapper)
	$(call need,python3)
	components/channels/tests/run.sh
	hack/dashboard/tests/run.sh
	python3 -m unittest discover -s components/bench -p 'test_*.py'
	python3 -m unittest discover -s components/bench/compare -p 'test_*.py'

test-integration: ## Suites that need Docker: kb against PostgreSQL+pgvector, memory-expiry job (throwaway containers)
	$(call need,docker)
	@docker info >/dev/null 2>&1 || { echo "ERROR: the Docker daemon is not reachable" >&2; exit 1; }
	$(MAKE) -C services/kb test-integration
	charts/openagentix/tests/memory-expiry-integration.sh

lint: lint-go lint-shell ## gofmt check, go vet, shellcheck

lint-go: ## gofmt -l and go vet in every Go module
	$(call need,go)
	@fail=0; for m in $(GO_MODULES); do \
	  echo "=== lint $$m"; \
	  out=$$(cd $$m && gofmt -l .); [ -z "$$out" ] || { echo "gofmt needed:"; echo "$$out"; fail=1; }; \
	  (cd $$m && go vet ./...) || fail=1; \
	done; exit $$fail

lint-shell: ## shellcheck on tracked *.sh at warning level; prints the count; non-blocking unless SHELLCHECK_STRICT=1
	$(call need,shellcheck)
	@out=$$(shellcheck -S warning -f gcc $(SHELL_FILES) 2>&1 || true); \
	  warns=$$(printf '%s\n' "$$out" | grep -c ': warning:' || true); errs=$$(printf '%s\n' "$$out" | grep -c ': error:' || true); \
	  [ -z "$$out" ] || printf '%s\n' "$$out"; \
	  echo "shellcheck: $$warns warning(s), $$errs error(s) in $$(echo $(SHELL_FILES) | wc -w | tr -d ' ') files"; \
	  [ "$(SHELLCHECK_STRICT)" != 1 ] || [ "$$((warns + errs))" = 0 ]

secrets: ## gitleaks over the whole git history, honouring .gitleaksignore
	$(call need,gitleaks)
	gitleaks git --no-banner --redact $(GITLEAKS_ARGS) .

test-console: ## Svelte console unit tests (vitest): not built yet
	@echo "test-console: not built yet. The console does not exist; see hack/ci/README.md for the job to enable." >&2; exit 1

e2e: ## Keyless end-to-end install on kind: not built yet
	@echo "e2e: not built yet. install.sh needs ANTHROPIC_API_KEY and TELEGRAM_BOT_TOKEN today; see hack/ci/E2E-PLAN.md." >&2; exit 1
