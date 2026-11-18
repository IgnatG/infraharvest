#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Prints the packages the tests workflow's race job runs with the cloud
# providers it builds by default (the minimal, aws and google tags): every
# package of the module but the ones listed below, so that a new package
# runs under the race detector without anyone adding it to a list.
#
# Usage: race-packages.sh (from the repository's root)
set -euo pipefail

cd "$(dirname "$0")/../.."
module=$(go list -m)

# The packages left out, and why.
excluded=(
  # The command and the MCP server take every provider's command, which
  # needs all three providers built: the job's second step runs them.
  internal/cli
  internal/mcpserver
  # Only built with the e2e tag, against real clouds: the e2e jobs run it.
  e2e
)

go list -tags minimal,aws,google ./... | while read -r package; do
  skip=
  for path in "${excluded[@]}"; do
    case "$package" in
      "$module/$path" | "$module/$path"/*) skip=1 ;;
    esac
  done
  [ -n "$skip" ] || echo "$package"
done
