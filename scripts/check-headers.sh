#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The licence lines below are what the script looks for, not this file's
# licence: REUSE skips them.
# REUSE-IgnoreStart

# Fails when a tracked Go file, shell script or workflow lacks
# infraharvest's licence line at the start of a line:
#   // SPDX-License-Identifier: AGPL-3.0-only   in a Go file
#   # SPDX-License-Identifier: AGPL-3.0-only    in a *.sh file or a
#                                              .github/workflows/*.yml file
# Every one of them is AGPL-3.0-only (see
# docs/development.md#licence-headers). The tests workflow runs it on every
# pull request, and scripts/fast-checks.sh before each commit.
#
# Usage: scripts/check-headers.sh [--cached] [<file>...]
#   --cached  check the files as the index has them, not the working tree
#   <file>    check only these files, relative to the repository's root;
#             other files are ignored (default: every tracked Go file,
#             shell script and workflow)
set -euo pipefail

cached=()
if [ "${1-}" = --cached ]; then
  cached=(--cached)
  shift
fi
if [ "${1-}" = -- ]; then
  shift
fi

# Go files take a // comment; shell scripts and workflows a # one.
go_specs=()
hash_specs=()
if [ $# -eq 0 ]; then
  go_specs=('*.go')
  hash_specs=('*.sh' '.github/workflows/*.yml')
else
  for file in "$@"; do
    case $file in
      *.go) go_specs+=(":(literal)$file") ;;
      *.sh | .github/workflows/*.yml) hash_specs+=(":(literal)$file") ;;
    esac
  done
  if [ ${#go_specs[@]} -eq 0 ] && [ ${#hash_specs[@]} -eq 0 ]; then
    echo "licence headers: no Go files, shell scripts or workflows to check"
    exit 0
  fi
fi

cd "$(dirname "$0")/.."

# missing <comment> <pathspec>... adds to out the files of pathspec with no
# licence line behind <comment>. git grep -L lists the files with no
# matching line. Its exit status says little here (1 can just mean nothing
# to list); anything above 1 is an error. The slashes are in brackets
# because Git Bash on Windows rewrites an argument that looks like a path
# before git sees it.
out=""
missing() {
  local comment=$1 status=0 found
  shift
  # With no pathspec, git grep would search every file.
  [ $# -gt 0 ] || return 0
  found=$(git grep ${cached[@]+"${cached[@]}"} -L -E -e "^$comment SPDX-License-Identifier: AGPL-3[.]0-only" -- "$@") || status=$?
  if [ "$status" -gt 1 ]; then
    echo "git grep failed with status $status" >&2
    exit "$status"
  fi
  if [ -n "$found" ]; then
    out+="$found"$'\n'
  fi
}
missing '[/][/]' ${go_specs[@]+"${go_specs[@]}"}
missing '#' ${hash_specs[@]+"${hash_specs[@]}"}

if [ -z "$out" ]; then
  echo "licence headers: every Go file, shell script and workflow carries the SPDX-License-Identifier: AGPL-3.0-only line"
  exit 0
fi
while IFS= read -r file; do
  case $file in
    *.go) header='"// Copyright <year> <name>" and "// SPDX-License-Identifier: AGPL-3.0-only"' ;;
    *) header='"# Copyright <year> <name>" and "# SPDX-License-Identifier: AGPL-3.0-only", after any #! line' ;;
  esac
  echo "::error file=$file::$file lacks the licence header. It starts with $header (see docs/development.md#licence-headers)."
done <<< "${out%$'\n'}"
exit 1
# REUSE-IgnoreEnd
