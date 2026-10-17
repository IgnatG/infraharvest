#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Checks that the workflows test with the engines the release image ships
# and agree on the scanners they install. The Dockerfile's
# hashicorp/terraform and opentofu/opentofu tags, which Dependabot bumps,
# are the source: every workflow's TERRAFORM_VERSION and TOFU_VERSION must
# match them, and every setup-tflint tflint_version and setup-trivy version
# must match the others.
#
# Usage: check-versions.sh (from the repository's root)
set -euo pipefail

status=0
fail() {
  echo "::error::$*"
  status=1
}

terraform=$(sed -nE 's/^FROM hashicorp\/terraform:([0-9][^@ ]*).*/\1/p' Dockerfile)
tofu=$(sed -nE 's/^FROM ghcr\.io\/opentofu\/opentofu:([0-9][^-@ ]*).*/\1/p' Dockerfile)
[ -n "$terraform" ] || fail "Dockerfile: no hashicorp/terraform tag found"
[ -n "$tofu" ] || fail "Dockerfile: no opentofu/opentofu tag found"
echo "Dockerfile: Terraform $terraform, OpenTofu $tofu"

# same <what> <want> <file:line:value>...: each value must be <want>, or,
# with no <want>, the first value.
same() {
  local what=$1 want=$2 entry
  shift 2
  for entry in "$@"; do
    local value=${entry##*:}
    [ -n "$want" ] || want=$value
    if [ "$value" != "$want" ]; then
      fail "${entry%:*}: $what is $value, want $want"
    fi
  done
}

mapfile -t found < <(grep -nE '^\s*TERRAFORM_VERSION: ' .github/workflows/*.yml | sed -E 's/:\s*TERRAFORM_VERSION: */:/')
same TERRAFORM_VERSION "$terraform" "${found[@]}"
mapfile -t found < <(grep -nE '^\s*TOFU_VERSION: ' .github/workflows/*.yml | sed -E 's/:\s*TOFU_VERSION: */:/')
same TOFU_VERSION "$tofu" "${found[@]}"
mapfile -t found < <(grep -nE '^\s*tflint_version: ' .github/workflows/*.yml | sed -E 's/:\s*tflint_version: */:/')
same "the tflint version" "" "${found[@]}"
# The version input on the line after each setup-trivy.
mapfile -t found < <(grep -nE -A2 'aquasecurity/setup-trivy@' .github/workflows/*.yml | sed -nE 's/^(.*)-([0-9]+)-\s*version: */\1:\2:/p')
same "the trivy version" "" "${found[@]}"

exit "$status"
