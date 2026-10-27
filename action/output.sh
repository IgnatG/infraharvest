# shellcheck shell=bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Writes a step's outputs to $GITHUB_OUTPUT and its annotations to the
# log, and checks the reports it reads, for the action's steps and scripts
# to source. A single-line value with a newline would end its output early
# and set another one, so it is refused; a multi-line value ends at a
# delimiter of random bytes, which no value can guess.

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
  delimiter="INFRAHARVEST_$(random_token)"
  {
    printf '%s<<%s\n' "$name" "$delimiter"
    if (($# > 0)); then printf '%s\n' "$@"; fi
    printf '%s\n' "$delimiter"
  } >> "${GITHUB_OUTPUT:?}"
}

# linked <path>: whether path, or a directory it is in, is a symbolic link,
# below the working directory, the repository the job checked out: one the
# repository commits, such as out/report/report.md to /proc/self/environ or
# out to a directory of the runner's, would lead what infraharvest writes,
# and what the action reads into the job summary, the pull request or a
# commit, elsewhere. A path outside the working directory, such as one
# under $RUNNER_TEMP, is the workflow's, not the repository's: it isn't
# checked.
linked() {
  local path=$1
  while [[ "$path" == */ ]]; do path=${path%/}; done
  if [[ "$path" == /* ]]; then
    if [[ "$path" != "$PWD"/* ]]; then return 1; fi
    path=${path#"$PWD"/}
  fi
  while [[ -n "$path" && "$path" != . ]]; do
    if [[ -L "$path" ]]; then return 0; fi
    if [[ "$path" != */* ]]; then return 1; fi
    path=${path%/*}
  done
  return 1
}

# random_token: 32 hex digits of random bytes.
random_token() {
  od -An -N16 -tx1 /dev/urandom | tr -d ' \n'
}

# annotation <error|warning|notice> <message>: the annotation, with the
# message's % and line breaks escaped as workflow commands take them. A
# message can carry what infraharvest, git or an input said, and a line
# break in it would start a line of its own, which the runner would take
# for a command, such as one that sets an output.
annotation() {
  local message=${2//%/%25}
  message=${message//$'\r'/%0D}
  message=${message//$'\n'/%0A}
  printf '::%s::%s\n' "$1" "$message"
}

# commands_off and commands_on: between them, the runner takes no workflow
# commands from the log, so that what a command prints there, which can
# come from the cloud or from the repository's files, can't set an output,
# an environment variable or a mask. commands_on's token is random, so
# nothing printed can turn commands on again; annotations wait until it.
commands_off() {
  commands_token="INFRAHARVEST_$(random_token)"
  printf '::stop-commands::%s\n' "$commands_token"
}
commands_on() {
  printf '::%s::\n' "${commands_token:?}"
}

# unlinked <file>: whether file is a regular file, itself no symbolic link,
# that no link the repository commits leads to (see linked): such as an
# out or report directory linked to the runner's home, which would put a
# file of the runner's in the job summary or the pull request.
unlinked() {
  [[ -f "$1" && ! -L "$1" ]] && ! linked "$1"
}
