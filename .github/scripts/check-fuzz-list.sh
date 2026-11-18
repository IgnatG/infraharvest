#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Checks that the tests workflow's fuzz job fuzzes every fuzz target in the
# repository: each `func FuzzXxx` in a _test.go file must be listed, with
# its package, in the job's list of "target package" lines, and every line
# of the list must name a target that exists. A target that isn't listed
# only ever runs its seed inputs.
#
# Usage: check-fuzz-list.sh (from the repository's root)
set -euo pipefail

cd "$(dirname "$0")/../.."
workflow=.github/workflows/test.yml

# The list is the here-document that follows "done <<'EOF'" in the fuzz job.
listed=$(awk '
  /^  fuzz:/ { job = 1; next }
  job && /^  [a-z][a-z-]*:/ { job = 0 }
  job && /<<.EOF.$/ { list = 1; next }
  job && list && /^ *EOF$/ { list = 0 }
  job && list { print $1, $2 }
' "$workflow" | sort)

found=$(grep -rEH --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\(' . |
  sed -E 's#^\./(.*)/[^/]+_test\.go:func (Fuzz[A-Za-z0-9_]*)\(.*#\2 ./\1/#' | sort)

if [ -z "$listed" ] || [ -z "$found" ]; then
  echo "::error::found no fuzz list in $workflow or no fuzz targets"
  exit 1
fi

status=0
while read -r line; do
  echo "::error::$line has no line in the fuzz job's list in $workflow"
  status=1
done < <(comm -13 <(echo "$listed") <(echo "$found"))
while read -r line; do
  echo "::error::$workflow's fuzz job lists $line, which isn't a fuzz target"
  status=1
done < <(comm -23 <(echo "$listed") <(echo "$found"))

[ "$status" -eq 0 ] && echo "All $(echo "$found" | wc -l) fuzz targets are in the fuzz job's list"
exit "$status"
