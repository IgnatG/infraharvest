# shellcheck shell=bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Writes a step's outputs to $GITHUB_OUTPUT, for the action's steps and
# scripts to source. A single-line value with a newline would end its
# output early and set another one, so it is refused; a multi-line value
# ends at a delimiter of random bytes, which no value can guess.

# output <name> <value>: the single-line output name.
output() {
  if [[ "$2" == *$'\n'* || "$2" == *$'\r'* ]]; then
    echo "::error::the $1 output can't hold a line break"
    return 1
  fi
  printf '%s=%s\n' "$1" "$2" >> "${GITHUB_OUTPUT:?}"
}

# multiline_output <name> <line>...: the output name, a line per argument.
multiline_output() {
  local name=$1 line delimiter
  shift
  for line in "$@"; do
    if [[ "$line" == *$'\n'* || "$line" == *$'\r'* ]]; then
      echo "::error::a line of the $name output can't hold a line break"
      return 1
    fi
  done
  delimiter="INFRAHARVEST_$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
  {
    printf '%s<<%s\n' "$name" "$delimiter"
    if (($# > 0)); then printf '%s\n' "$@"; fi
    printf '%s\n' "$delimiter"
  } >> "${GITHUB_OUTPUT:?}"
}
