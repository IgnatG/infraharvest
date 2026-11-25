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
# And it writes aws, google and azure: whether that cloud's e2e jobs run.
# A core file that only one cloud's build reads (its listers, its command,
# its catalogue data, its state store, its e2e tests and their testdata)
# runs that cloud's e2e jobs alone; any other core file runs all three.
#
# And go_lint: whether the pull request changes what golangci-lint reads (Go
# files, go.mod, go.sum, .golangci.json) or how MegaLinter runs it
# (.mega-linter.yml and its workflow). The megalinter workflow skips the Go
# linters when it is false; the other linters always run.
#
# Without a base and a head (a push or workflow_dispatch), or when git can't
# tell what changed, code=true, permissions=true, go_lint=true, and every
# cloud is true.
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

# go_lint reports whether the file at path $1 is one the Go linters read, or
# the one that sets how they run.
go_lint() {
  case $1 in
    *.go | go.mod | go.sum | .golangci.json) return 0 ;;
    .mega-linter.yml | .github/workflows/megalinter.yml) return 0 ;;
  esac
  return 1
}

# cloud_of prints the cloud whose build alone reads the core file at path
# $1: aws, google or azure; nothing when every cloud's build reads it. An
# e2e file is that cloud's when its build constraint names that cloud
# alone, as e2e/aws_test.go's does; a deleted one is everyone's.
cloud_of() {
  case $1 in
    internal/providers/aws/* | internal/catalogue/data/aws/* | pkg/tfstate/stores/s3/*) echo aws ;;
    internal/providers/google/* | internal/catalogue/data/google/* | pkg/tfstate/stores/gcs/*) echo google ;;
    internal/providers/azure/* | internal/catalogue/data/azurerm/* | internal/catalogue/data/azapi/* | pkg/tfstate/stores/azblob/*) echo azure ;;
    internal/cli/providers/provider_cmd_aws.go | internal/cli/providers/aws_*_test.go) echo aws ;;
    internal/cli/providers/provider_cmd_google.go | internal/cli/providers/google_*_test.go) echo google ;;
    internal/cli/providers/provider_cmd_azure.go | internal/cli/providers/azure_*_test.go) echo azure ;;
    e2e/testdata/aws/* | e2e/testdata/aws-*) echo aws ;;
    e2e/testdata/google/* | e2e/testdata/google-*) echo google ;;
    e2e/testdata/azure/* | e2e/testdata/azure-*) echo azure ;;
    e2e/*.go)
      if [ -f "$1" ]; then
        sed -En 's/^\/\/go:build .*\(!minimal \|\| (aws|google|azure)\)$/\1/p;/^package /q' "$1"
      fi
      ;;
  esac
}

code=true
permissions=true
aws=true
google=true
azure=true
golint=true
# The merge base to the head: what the pull request changes, whatever main
# gained since. Names only, so a partial clone fetches no file contents.
if [ -n "${BASE-}" ] && [ -n "${HEAD-}" ] && files=$(git diff --name-only --no-renames "$BASE...$HEAD"); then
  code=false
  permissions=false
  aws=false
  google=false
  azure=false
  golint=false
  while IFS= read -r file; do
    [ -n "$file" ] || continue
    if ! not_code "$file"; then
      cloud=$(cloud_of "$file")
      echo "changes code${cloud:+ of $cloud}: $file"
      code=true
      case $cloud in
        aws) aws=true ;;
        google) google=true ;;
        azure) azure=true ;;
        *)
          aws=true
          google=true
          azure=true
          ;;
      esac
    fi
    if go_lint "$file"; then
      echo "changes the Go linters' input: $file"
      golint=true
    fi
    case $file in
      permissions/*)
        echo "changes the permissions: $file"
        permissions=true
        ;;
    esac
  done <<< "$files"
fi
outputs="code=$code
permissions=$permissions
aws=$aws
google=$google
azure=$azure
go_lint=$golint"
echo "$outputs"
echo "$outputs" >> "${GITHUB_OUTPUT:?}"
