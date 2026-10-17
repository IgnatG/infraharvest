#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Checks that the workflows test with the engines the release image ships
# and agree on the scanners they install. The Dockerfile's
# hashicorp/terraform and opentofu/opentofu tags, which Dependabot bumps,
# are the source: the shared setup action (.github/actions/setup-engines)
# and any workflow that still sets a Terraform or OpenTofu version of its
# own must match them, and every setup-tflint tflint_version and
# setup-trivy version must match the others.
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

files=(.github/workflows/*.yml .github/actions/*/action.yml)
action=.github/actions/setup-engines/action.yml

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

# found <key>: file:line:value for each literal "<key>: value" in the
# workflows and actions; values taken from an expression are left out.
found() {
  grep -nE "^\s*$1: " "${files[@]}" | sed -E "s/:\s*$1: */:/" | grep -v ':[$]{{' || true
}

# The setup action must name both engines, or the checks below pass on
# nothing.
grep -qE '^\s*terraform_version: [0-9]' "$action" || fail "$action: no terraform_version found"
grep -qE '^\s*tofu_version: [0-9]' "$action" || fail "$action: no tofu_version found"

mapfile -t entries < <(found TERRAFORM_VERSION; found terraform_version)
same "the Terraform version" "$terraform" "${entries[@]}"
mapfile -t entries < <(found TOFU_VERSION; found tofu_version)
same "the OpenTofu version" "$tofu" "${entries[@]}"
mapfile -t entries < <(found tflint_version)
same "the tflint version" "" "${entries[@]}"
# The version input on the line after each setup-trivy.
mapfile -t entries < <(grep -nE -A2 'aquasecurity/setup-trivy@' "${files[@]}" | sed -nE 's/^(.*)-([0-9]+)-\s*version: */\1:\2:/p')
same "the trivy version" "" "${entries[@]}"

exit "$status"
