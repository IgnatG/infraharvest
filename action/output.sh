# shellcheck shell=bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Writes a step's outputs to $GITHUB_OUTPUT, and checks the reports it
# reads, for the action's steps and scripts to source. A single-line value with a newline would end its
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

# unlinked <file>: whether file is a regular file that no symbolic link
# leads to from the working directory: neither file nor a directory on the
# way to it is a link. A repository can commit one, such as an out or
# report directory linked to the runner's home, that would put a file of
# the runner's in the job summary or the pull request. Of an absolute path
# outside the working directory, where the repository has nothing, only
# the file itself is checked.
unlinked() {
  local file=$1 path= part
  local -a parts
  if [[ "$file" == "$PWD"/* ]]; then
    file=${file#"$PWD"/}
  elif [[ "$file" == /* ]]; then
    path=$(dirname -- "$file")
    file=$(basename -- "$file")
  fi
  IFS=/ read -r -a parts <<< "$file"
  for part in "${parts[@]}"; do
    [[ -z "$part" || "$part" == . ]] && continue
    path=${path:+${path%/}/}$part
    [[ -L "$path" ]] && return 1
  done
  [[ -n "$path" && -f "$path" ]]
}
