#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The licence lines below are what the script looks for, not this file's
# licence: REUSE skips them.
# REUSE-IgnoreStart

# Fails when a tracked Go file lacks infraharvest's licence line,
#   // SPDX-License-Identifier: AGPL-3.0-only
# at the start of a line. Every Go file in the repository is AGPL-3.0-only
# (see docs/development.md#licence-headers). The tests workflow runs it on
# every pull request, and scripts/fast-checks.sh before each commit.
#
# Usage: scripts/check-headers.sh [--cached] [<file>...]
#   --cached  check the files as the index has them, not the working tree
#   <file>    check only these files, relative to the repository's root;
#             files that aren't Go are ignored (default: every tracked Go file)
set -euo pipefail

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
  pathspec=('*.go')
else
  for file in "$@"; do
    case $file in
      *.go) pathspec+=(":(literal)$file") ;;
    esac
  done
  if [ ${#pathspec[@]} -eq 0 ]; then
    echo "licence headers: no Go files to check"
    exit 0
  fi
fi

cd "$(dirname "$0")/.."

# git grep -L lists the files with no matching line. Its exit status says
# little here (1 can just mean nothing to list); anything above 1 is an
# error. The slashes are in brackets because Git Bash on Windows rewrites
# an argument that looks like a path before git sees it.
status=0
out=$(git grep ${cached[@]+"${cached[@]}"} -L -E -e '^[/][/] SPDX-License-Identifier: AGPL-3[.]0-only' -- "${pathspec[@]}") || status=$?
if [ "$status" -gt 1 ]; then
  echo "git grep failed with status $status" >&2
  exit "$status"
fi

if [ -z "$out" ]; then
  echo "licence headers: every Go file carries the SPDX-License-Identifier: AGPL-3.0-only line"
  exit 0
fi
while IFS= read -r file; do
  echo "::error file=$file::$file lacks the licence header. A Go file starts with \"// Copyright <year> <name>\" and \"// SPDX-License-Identifier: AGPL-3.0-only\" (see docs/development.md#licence-headers)."
done <<< "$out"
exit 1
# REUSE-IgnoreEnd
