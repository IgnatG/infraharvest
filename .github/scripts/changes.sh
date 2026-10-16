#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Writes code=true to $GITHUB_OUTPUT when a pull request changes more than
# documentation, linter configuration and workflows other than the tests
# workflow, and code=false when it doesn't: the tests workflow then skips
# its Go and e2e jobs. Without a base and a head (workflow_dispatch), or when
# git can't tell what changed, code=true.
#
# Usage: BASE=<sha> HEAD=<sha> changes.sh
set -euo pipefail

# not_code reports whether the file at path $1 is one no Go or e2e job reads.
not_code() {
  case $1 in
    # The CLI's tests check these generated pages.
    docs/reference/*) return 1 ;;
    # A change to the tests workflow runs every job.
    .github/workflows/test.yml) return 1 ;;
    *.md | docs/* | mkdocs.yml) return 0 ;;
    .mega-linter.yml | .markdownlint.yaml | .markdownlint-cli2.yaml | .golangci.json | .editorconfig | .coderabbit.yaml) return 0 ;;
    .github/actionlint.yaml | .github/linters/*) return 0 ;;
    .github/workflows/* | .github/ISSUE_TEMPLATE/* | .github/CODEOWNERS | .github/dependabot.yml) return 0 ;;
  esac
  return 1
}

code=true
# The merge base to the head: what the pull request changes, whatever main
# gained since. Names only, so a partial clone fetches no file contents.
if [ -n "${BASE-}" ] && [ -n "${HEAD-}" ] && files=$(git diff --name-only --no-renames "$BASE...$HEAD"); then
  code=false
  while IFS= read -r file; do
    if [ -n "$file" ] && ! not_code "$file"; then
      echo "changes code: $file"
      code=true
    fi
  done <<< "$files"
fi
echo "code=$code"
echo "code=$code" >> "${GITHUB_OUTPUT:?}"
