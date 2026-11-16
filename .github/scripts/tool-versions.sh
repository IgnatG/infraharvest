#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Lists the tool versions the repository pins where no updater looks
# (Dependabot reads go.mod, the actions, the Dockerfile, the e2e compose
# file and docs/requirements.txt, but not a version in a run step, a
# script or a with: input) and that a newer release has passed. The weekly
# tool versions workflow opens an issue with the list.
#
# Each line of the output is a Markdown table row: the tool, the version
# pinned, the latest, and where it is pinned. Nothing is printed for a tool
# that is current. A tool whose latest version can't be looked up gets a
# row saying so, and the script still exits 0: it reports, it never fails
# a run.
#
# Usage: tool-versions.sh (from the repository's root, with gh, curl and
# jq on PATH, and GH_TOKEN for gh).
set -euo pipefail

# pinned <file> <sed -E script>: the first version the script prints from
# the file.
pinned() {
  sed -nE "$2" "$1" | head -n 1
}

# after <file> <pattern> <sed -E script>: pinned, in the lines just after
# the first line matching pattern.
after() {
  grep -A4 -m1 -E "$2" "$1" | sed -nE "$3" | head -n 1
}

latest() {
  local kind=$1 source=$2
  case $kind in
    github) gh api "repos/$source/releases/latest" --jq .tag_name ;;
    goproxy) curl -sSfL --retry 3 "https://proxy.golang.org/$source/@latest" | jq -r .Version ;;
    npm) curl -sSfL --retry 3 "https://registry.npmjs.org/$source/latest" | jq -r .version ;;
    go) curl -sSfL --retry 3 'https://go.dev/dl/?mode=json' | jq -r '.[0].version' ;;
  esac
}

# check <tool> <pinned> <kind> <source> <where>
check() {
  local tool=$1 have=$2 kind=$3 source=$4 where=$5 want
  if [ -z "$have" ]; then
    echo "| $tool | not found | | \`$where\`: the pattern this script reads it with no longer matches |"
    return
  fi
  if ! want=$(latest "$kind" "$source" 2>/dev/null) || [ -z "$want" ] || [ "$want" = null ]; then
    echo "| $tool | $have | couldn't be looked up | \`$where\` |"
    return
  fi
  # Compare without the leading v (or go) the sources differ on.
  local h=${have#v} w=${want#v}
  h=${h#go} w=${w#go}
  if [ "$h" != "$w" ]; then
    echo "| $tool | $have | $want | \`$where\` |"
  fi
}

check GoReleaser "$(pinned .github/workflows/release.yml 's/^\s*version: (v[0-9][^ ]*)$/\1/p')" \
  github goreleaser/goreleaser .github/workflows/release.yml
check conftest "$(after .github/workflows/test.yml 'name: Install conftest' 's/^\s*VERSION: ([0-9][^ ]*)$/\1/p')" \
  github open-policy-agent/conftest .github/workflows/test.yml
check TFLint "$(pinned .github/actions/setup-engines/action.yml 's/^\s*tflint_version: (v?[0-9][^ ]*)$/\1/p')" \
  github terraform-linters/tflint .github/actions/setup-engines/action.yml
check Trivy "$(after .github/actions/setup-engines/action.yml 'aquasecurity/setup-trivy@' 's/^\s*version: (v?[0-9][^ ]*)$/\1/p')" \
  github aquasecurity/trivy .github/actions/setup-engines/action.yml
check golangci-lint "$(pinned .mega-linter.yml 's/^\s*golangci_lint_version=([0-9][^ ]*)$/\1/p')" \
  github golangci/golangci-lint .mega-linter.yml
check MegaLinter "$(pinned .github/workflows/megalinter.yml 's/^.*uses: docker:\/\/ghcr\.io\/oxsecurity\/megalinter-go@sha256:[0-9a-f]+ # (v[0-9][^ ]*)$/\1/p')" \
  github oxsecurity/megalinter .github/workflows/megalinter.yml
check govulncheck "$(pinned .github/scripts/govulncheck-gate.sh 's/^.*golang\.org\/x\/vuln\/cmd\/govulncheck@(v[0-9][^ ]*) .*$/\1/p')" \
  goproxy golang.org/x/vuln .github/scripts/govulncheck-gate.sh
check benchstat "$(pinned .github/workflows/remote.yml 's/^.*golang\.org\/x\/perf\/cmd\/benchstat@(v[0-9][^ ]*) .*$/\1/p')" \
  goproxy golang.org/x/perf .github/workflows/remote.yml
check markdownlint-cli2 "$(pinned scripts/fast-checks.sh 's/^MARKDOWNLINT_CLI2_VERSION=([0-9][^ ]*)$/\1/p')" \
  npm markdownlint-cli2 scripts/fast-checks.sh
check "Go toolchain" "$(pinned go.mod 's/^toolchain (go[0-9][^ ]*)$/\1/p')" \
  go go go.mod
