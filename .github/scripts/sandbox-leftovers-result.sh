#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The last step of each cloud's job in sandbox-leftovers.yml: puts what the
# leftovers scripts found ($LEFTOVERS_FILE, one per line, names and IDs
# only) in the step's leftovers output, for the issue, and in the run's
# summary, and fails when it isn't empty.
#
# Usage: LEFTOVERS_FILE=<file> sandbox-leftovers-result.sh <cloud>
set -euo pipefail

cloud=${1:?usage: $0 <cloud>}
left=""
if [ -f "${LEFTOVERS_FILE:?}" ]; then
  left=$(sed '/^$/d' "$LEFTOVERS_FILE" | sort -u)
fi
delimiter="EOF_$(openssl rand -hex 16)"
{
  echo "leftovers<<$delimiter"
  printf '%s\n' "$left"
  echo "$delimiter"
} >> "$GITHUB_OUTPUT"
{
  echo "## $cloud"
  echo
  if [ -n "$left" ]; then
    echo '```text'
    printf '%s\n' "$left"
    echo '```'
  else
    echo "Nothing left."
  fi
} >> "$GITHUB_STEP_SUMMARY"
if [ -n "$left" ]; then
  echo "::error::Left in $cloud: $(wc -l <<< "$left") (see the summary)"
  exit 1
fi
