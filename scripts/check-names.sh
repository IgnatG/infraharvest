#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Fails when a tracked file uses one of the names below, in any case: an
# old project name, its resource label prefix and its utility package,
# none of which infraharvest uses. The pattern is built from pieces so that
# this script doesn't match it. A file that must mention one of them goes
# into .github/names-allowlist.txt, with the reason. The tests workflow
# runs it on every pull request, and scripts/fast-checks.sh before each
# commit.
#
# Usage: scripts/check-names.sh [--cached] [<file>...]
#   --cached  check the files as the index has them, not the working tree
#   <file>    check only these files, relative to the repository's root
#             (default: every tracked file)
set -euo pipefail

pattern="terra""former|tf""er--|terraform""utils"
allowlist=.github/names-allowlist.txt

cached=()
if [ "${1-}" = --cached ]; then
  cached=(--cached)
  shift
fi
if [ "${1-}" = -- ]; then
  shift
fi

pathspec=()
if [ $# -eq 0 ]; then
  pathspec=(.)
else
  for file in "$@"; do
    pathspec+=(":(literal)$file")
  done
fi

cd "$(dirname "$0")/.."

# Each allowlist entry is a pathspec, such as a path or a glob, left out of
# the search. Comments and blank lines are skipped.
if [ -f "$allowlist" ]; then
  while IFS= read -r entry; do
    pathspec+=(":(exclude)$entry")
  done < <(grep -vE '^[[:space:]]*(#|$)' "$allowlist" || true)
fi

# git grep exits 1 when nothing matches, which is the passing case;
# anything above 1 is an error.
status=0
out=$(git grep ${cached[@]+"${cached[@]}"} -n -I -i -E -e "$pattern" -- "${pathspec[@]}") || status=$?
if [ "$status" -gt 1 ]; then
  echo "git grep failed with status $status" >&2
  exit "$status"
fi

if [ -z "$out" ]; then
  echo "names: no tracked file uses a name from scripts/check-names.sh"
  exit 0
fi
while IFS=: read -r file line text; do
  echo "::error file=$file,line=$line::$file:$line uses a name infraharvest doesn't use (see scripts/check-names.sh): $text"
done <<< "$out"
exit 1
