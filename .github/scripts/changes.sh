#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Writes code=true to $GITHUB_OUTPUT when a pull request changes core code:
# anything the Go, e2e and build jobs of the tests workflow read (Go files,
# go.mod and go.sum, the Makefile, the catalogue's data and other embedded
# files, testdata, e2e/, the composite actions and scripts those jobs run,
# and the tests workflow itself). It writes code=false when the pull request
# changes only files no such job reads: documentation, Markdown, licences,
# linter configuration, other workflows and the files only they read, and
# the permission templates. The tests workflow then skips those jobs.
#
# It also writes permissions=true when the pull request changes the
# permission templates (permissions/), which the permissions job checks.
#
# Without a base and a head (a push or workflow_dispatch), or when git can't
# tell what changed, code=true and permissions=true.
#
# Usage: BASE=<sha> HEAD=<sha> changes.sh
set -euo pipefail

# not_code reports whether the file at path $1 is one no Go, e2e or build
# job reads.
not_code() {
  case $1 in
    # The CLI's tests check these generated pages.
    docs/reference/*) return 1 ;;
    # A change to the tests workflow, or to this script, runs every job.
    .github/workflows/test.yml | .github/scripts/changes.sh) return 1 ;;
    *.md | docs/* | mkdocs.yml) return 0 ;;
    LICENSE | LICENSES/* | REUSE.toml | .gitignore | .gitattributes) return 0 ;;
    .mega-linter.yml | .markdownlint.yaml | .markdownlint-cli2.yaml | .golangci.json | .editorconfig | .coderabbit.yaml) return 0 ;;
    .github/actionlint.yaml | .github/linters/* | .github/names-allowlist.txt) return 0 ;;
    .github/workflows/* | .github/ISSUE_TEMPLATE/* | .github/CODEOWNERS | .github/dependabot.yml) return 0 ;;
    # The licences and names job, which always runs, and the sandboxes.
    scripts/check-headers.sh | scripts/check-names.sh | scripts/fast-checks.sh | scripts/hooks/*) return 0 ;;
    .github/scripts/check-versions.sh | .github/scripts/sandbox-*) return 0 ;;
    # The release and its image, which the release-check workflow builds.
    .goreleaser.yaml | Dockerfile | install.sh) return 0 ;;
    # Checked by the permissions job (see permissions below).
    permissions/*) return 0 ;;
  esac
  return 1
}

code=true
permissions=true
# The merge base to the head: what the pull request changes, whatever main
# gained since. Names only, so a partial clone fetches no file contents.
if [ -n "${BASE-}" ] && [ -n "${HEAD-}" ] && files=$(git diff --name-only --no-renames "$BASE...$HEAD"); then
  code=false
  permissions=false
  while IFS= read -r file; do
    [ -n "$file" ] || continue
    if ! not_code "$file"; then
      echo "changes code: $file"
      code=true
    fi
    case $file in
      permissions/*)
        echo "changes the permissions: $file"
        permissions=true
        ;;
    esac
  done <<< "$files"
fi
echo "code=$code"
echo "permissions=$permissions"
{
  echo "code=$code"
  echo "permissions=$permissions"
} >> "${GITHUB_OUTPUT:?}"
