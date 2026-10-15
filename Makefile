# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Local checks, and shortcuts to the workflows that run the heavy ones on
# GitHub. `make` lists the targets; docs/development.md explains them.
#
# The fast checks never build the module. lint, vet, test and golden
# compile it, which takes several GB of RAM and a few minutes, so most
# changes are checked on GitHub instead, with the ci-* targets.

SHELL := bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

# Go packages to lint, vet or test, here or on GitHub.
PKG ?= ./...
# Build tags, such as minimal,aws to compile only the AWS provider.
TAGS ?=
# The branch the ci-* targets run on: the current one. Push it first.
REF ?= $(shell git rev-parse --abbrev-ref HEAD)
# The runner ci-check runs on: ubuntu-latest, windows-latest or macos-latest.
# (Not OS, which Windows sets in the environment.)
RUNNER ?= ubuntu-latest
# The repository the workflows run in, whatever the remotes are called.
GH_REPO ?= IgnatG/infraharvest
# A workflow run's ID, for ci-watch and ci-download; empty means the latest
# run of WORKFLOW on REF.
RUN ?=
WORKFLOW ?= remote
# The artifact ci-download fetches: golden, or go-mod from ci-tidy.
NAME ?= golden

# The golden tests to rewrite: the catalogue's, unless PKG is given.
GOLDEN_PKG := $(if $(filter command line environment,$(origin PKG)),$(PKG),./internal/catalogue)
# The golangci-lint version CI runs, read from the MegaLinter configuration.
GOLANGCI_LINT_VERSION := v$(shell sed -n 's/^ *golangci_lint_version=//p' .mega-linter.yml)

GO_TAGS := $(if $(TAGS),-tags $(TAGS))
LINT_TAGS := $(if $(TAGS),--build-tags $(TAGS))

.PHONY: help hooks tools fast staged fmt lint vet test golden \
  ci-check ci-golden ci-tidy ci-lint ci-watch ci-download

##@ Fast checks (no build)

help: ## List the targets
	@awk 'BEGIN { FS = ":.*## " } \
	    /^##@/ { printf "\n%s\n", substr($$0, 5); next } \
	    /^[a-z%-]+:.*## / { printf "  %-16s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf '\nVariables: PKG=%s TAGS=%s REF=%s RUNNER=%s RUN=%s NAME=%s\n' '$(PKG)' '$(TAGS)' '$(REF)' '$(RUNNER)' '$(RUN)' '$(NAME)'

hooks: ## Run the fast checks before each commit, and check commit messages
	git config core.hooksPath scripts/hooks
	@echo "Hooks enabled from scripts/hooks; git config --unset core.hooksPath turns them off."

tools: ## Show which check tools are installed, and how to get the others
	@check() { \
	    if command -v "$$1" >/dev/null 2>&1; then printf '  %-14s %s\n' "$$1" "$$(command -v "$$1")"; \
	    else printf '  %-14s missing: %s\n' "$$1" "$$2"; fi; \
	}; \
	check gofmt "comes with Go, https://go.dev/dl/"; \
	check goimports "go install golang.org/x/tools/cmd/goimports@latest"; \
	check shellcheck "https://github.com/koalaman/shellcheck#installing"; \
	check actionlint "https://github.com/rhysd/actionlint/blob/main/docs/install.md"; \
	check npx "Node.js, for markdownlint-cli2: https://nodejs.org/"; \
	check gh "https://cli.github.com/, for the ci-* targets"; \
	check golangci-lint "curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b \"\$$(go env GOPATH)/bin\" $(GOLANGCI_LINT_VERSION)"

fast: ## Every fast check on every tracked file
	scripts/fast-checks.sh --all

fast-%: ## One fast check on every tracked file, such as fast-shellcheck
	scripts/fast-checks.sh --all --only $*

staged: ## The fast checks on the staged files, as the pre-commit hook runs them
	scripts/fast-checks.sh --staged

fmt: ## gofmt -s and goimports the Go files changed since the last commit (rewrites them)
	@files=$$(git diff --name-only --diff-filter=ACMR HEAD -- '*.go'); \
	if [ -z "$$files" ]; then echo "no Go file changed"; exit 0; fi; \
	gofmt -s -w $$files; \
	goimports -format-only -local github.com/IgnatG/infraharvest -w $$files; \
	printf 'formatted:\n%s\n' "$$files"

##@ Full checks (compile the module; CI runs them on every pull request)

lint: ## golangci-lint on PKG, with the repository's config and CI's version
	@have=$$(golangci-lint version --short 2>/dev/null || echo none); \
	if [ "v$${have#v}" != "$(GOLANGCI_LINT_VERSION)" ]; then \
	    echo "warning: golangci-lint $$have is installed; CI runs $(GOLANGCI_LINT_VERSION), whose findings may differ:"; \
	    echo "  curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b \"\$$(go env GOPATH)/bin\" $(GOLANGCI_LINT_VERSION)"; \
	fi
	golangci-lint run $(LINT_TAGS) $(PKG)

vet: ## go vet PKG with TAGS
	go vet $(GO_TAGS) $(PKG)

test: ## go test PKG with TAGS, such as make test PKG=./internal/engine/...
	go test $(GO_TAGS) $(PKG)

golden: ## Rewrite the golden files of PKG (default ./internal/catalogue); review the diff
	go test $(GOLDEN_PKG) -run Golden -update

##@ On GitHub (push REF first; needs gh)

# dispatch runs workflow $(1) on REF with the inputs $(2), waits for the run
# to show up, and prints its ID and address.
define dispatch
@before=$$(gh run list -R $(GH_REPO) --workflow $(1) --branch $(REF) --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId // 0'); \
  gh workflow run $(1) -R $(GH_REPO) --ref $(REF) $(2); \
  for _ in $$(seq 20); do \
    run=$$(gh run list -R $(GH_REPO) --workflow $(1) --branch $(REF) --event workflow_dispatch --limit 1 --json databaseId,url --jq ".[0] | select(.databaseId != $$before) | \"\(.databaseId) \(.url)\""); \
    if [ -n "$$run" ]; then \
        echo "run $${run% *}: $${run#* }"; \
        echo "follow it with: make ci-watch RUN=$${run% *}"; \
        exit 0; \
    fi; \
    sleep 3; \
  done; \
  echo "the run hasn't shown up yet: gh run list -R $(GH_REPO) --workflow $(1) --branch $(REF)"
endef

ci-check: ## go vet and go test PKG on GitHub, on RUNNER (remote workflow, task check)
	$(call dispatch,remote,-f task=check -f packages="$(PKG)" -f os=$(RUNNER))

ci-golden: ## Rewrite the golden files of PKG on GitHub (remote workflow, task golden); then ci-download
	$(call dispatch,remote,-f task=golden -f packages="$(GOLDEN_PKG)")

ci-tidy: ## go mod tidy on GitHub (remote workflow, task tidy); then ci-download NAME=go-mod
	$(call dispatch,remote,-f task=tidy)

ci-lint: ## Every linter, golangci-lint included, on the whole repository on GitHub (megalinter workflow)
	$(call dispatch,megalinter,)

ci-watch: ## Watch run RUN, or the latest run of WORKFLOW on REF, until it ends
	@run='$(RUN)'; \
	if [ -z "$$run" ]; then \
	    run=$$(gh run list -R $(GH_REPO) --workflow $(WORKFLOW) --branch $(REF) --limit 1 --json databaseId --jq '.[0].databaseId'); \
	fi; \
	gh run watch "$$run" -R $(GH_REPO) --exit-status

ci-download: ## Download artifact NAME of run RUN into the working tree, to review and commit
	@if [ -z '$(RUN)' ]; then echo "set RUN to the run's ID (make ci-golden and make ci-tidy print it)"; exit 1; fi
	cd "$$(git rev-parse --show-toplevel)" && gh run download $(RUN) -R $(GH_REPO) -n $(NAME)
	git status --short
