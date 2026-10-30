#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Fails when a sandbox estate creates a kind of resource its cloud's sweep
# doesn't account for: every `resource "<type>"` in
# e2e/testdata/sandbox/<cloud> and e2e/testdata/sandbox/<cloud>-* (not the
# bootstraps, which the sweeps never touch) must be listed in
# .github/scripts/sandbox-<cloud>-sweep.sh, on a "# sweep-coverage swept:"
# line, when the sweep deletes it, or a "# sweep-coverage with-parent:"
# line, when it goes with what holds it. Without this, an estate could
# gain a kind that a failed run leaves behind, billed until someone
# notices. Types of providers that make nothing in a cloud (random, tls,
# null, time, local, archive, http, external, terraform_data) don't count;
# a type of any other provider than the cloud's fails, as the check can't
# tell what it makes.
#
# It reads files alone: no cloud, no credentials. The Sandbox coverage
# workflow runs it on pull requests that change the estates or the sweeps.
#
# Usage: .github/scripts/sandbox-sweep-coverage.sh
set -euo pipefail

cd "$(dirname "$0")/../.."
status=0
for cloud in aws azure google; do
  case "$cloud" in
    aws) providers='aws_|awscc_' ;;
    azure) providers='azurerm_|azapi_|azuread_' ;;
    google) providers='google_' ;;
  esac
  sweep=.github/scripts/sandbox-$cloud-sweep.sh
  declared=$(sed -nE 's/^# sweep-coverage (swept|with-parent): //p' "$sweep" | tr ' ' '\n' | sed '/^$/d' | sort -u)
  dirs=()
  for dir in e2e/testdata/sandbox/"$cloud" e2e/testdata/sandbox/"$cloud"-*; do
    if [ -d "$dir" ] && [ "$dir" != "e2e/testdata/sandbox/$cloud-bootstrap" ]; then
      dirs+=("$dir")
    fi
  done
  if [ "${#dirs[@]}" -eq 0 ]; then
    echo "::error::No $cloud estates under e2e/testdata/sandbox"
    status=1
    continue
  fi
  types=$(find "${dirs[@]}" -name '*.tf' -exec sed -nE 's/^[[:space:]]*resource[[:space:]]+"([A-Za-z0-9_-]+)".*/\1/p' {} + | sort -u)
  missing=()
  foreign=()
  while IFS= read -r type; do
    case "$type" in
      '' | random_* | tls_* | null_* | time_* | local_* | archive_* | http | http_* | external | terraform_data) continue ;;
    esac
    if ! [[ "$type" =~ ^($providers) ]]; then
      foreign+=("$type")
    elif ! grep -qxF "$type" <<< "$declared"; then
      missing+=("$type")
    fi
  done <<< "$types"
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "::error file=$sweep::The $cloud estates create ${missing[*]}, which $sweep neither sweeps nor lists as going with what holds it. Delete them in the sweep and add them to a '# sweep-coverage swept:' line, or, if they go with their parent, to a '# sweep-coverage with-parent:' line."
    status=1
  fi
  if [ "${#foreign[@]}" -gt 0 ]; then
    echo "::error::The $cloud estates use ${foreign[*]}, of a provider the check doesn't know: add it to the cloud's providers, or to the providers that make nothing in a cloud, in $0."
    status=1
  fi
  if [ "${#missing[@]}" -eq 0 ] && [ "${#foreign[@]}" -eq 0 ]; then
    echo "$cloud: the sweep accounts for all $(grep -c . <<< "$types") resource types of ${dirs[*]}"
  fi
done
exit "$status"
