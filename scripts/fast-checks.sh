#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Fast checks that never build the module: Go formatting and import
# grouping, licence headers, names the repository doesn't use, shell
# scripts, workflows and Markdown. The pre-commit hook
# (scripts/hooks/pre-commit) runs them on the files a commit stages, as they
# are staged; `make fast` runs them on every tracked file.
#
# Every tool is optional: a check whose tool isn't installed is skipped with
# a hint on how to install it. CI runs the same checks (MegaLinter, in the
# megalinter workflow, and the tests workflow), so a skipped check is caught
# there; the hook only catches it sooner. See docs/development.md.
set -euo pipefail

# markdownlint-cli2 reads the rules MegaLinter's markdownlint-cli does
# (.markdownlint.yaml).
MARKDOWNLINT_CLI2_VERSION=0.23.2
# goimports puts this module's imports in a group of their own, after the
# third-party ones.
LOCAL_PREFIX=github.com/infraharvest/infraharvest-cli
CHECKS="gofmt goimports headers names shellcheck actionlint markdownlint"

usage() {
  cat <<EOF
Usage: scripts/fast-checks.sh [--staged | --all] [--only <check>[,<check>...]]

  --staged  check the files the next commit stages, as staged (the default)
  --all     check every tracked file, as it is in the working tree
  --only    run only these checks: ${CHECKS// /, }
EOF
}

mode=staged
only=
while [ $# -gt 0 ]; do
  case $1 in
    --staged) mode=staged ;;
    --all) mode=all ;;
    --only)
      if [ $# -lt 2 ]; then
        echo "--only needs a comma-separated list of checks" >&2
        exit 2
      fi
      only=$2
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

for check in ${only//,/ }; do
  case " $CHECKS " in
    *" $check "*) ;;
    *)
      echo "unknown check: $check (one of: $CHECKS)" >&2
      exit 2
      ;;
  esac
done

root=$(git rev-parse --show-toplevel)
cd "$root"

status=0

enabled() {
  [ -z "$only" ] && return 0
  case ",$only," in
    *",$1,"*) return 0 ;;
    *) return 1 ;;
  esac
}

have() {
  command -v "$1" >/dev/null 2>&1
}

pass() {
  printf 'ok    %s\n' "$*"
}

skip() {
  printf 'skip  %s\n' "$*"
}

fail() {
  printf 'FAIL  %s\n' "$*"
  status=1
}

# indent prints its input indented, under the line that names the check.
indent() {
  sed 's/^/      /'
}

# The files to check. Deleted files are left out; renamed ones count under
# their new name.
files=()
added=()
if [ "$mode" = staged ]; then
  while IFS= read -r -d '' f; do files+=("$f"); done < <(git diff --cached --name-only --diff-filter=ACMR -z)
  while IFS= read -r -d '' f; do added+=("$f"); done < <(git diff --cached --name-only --diff-filter=A -z -- '*.go')
else
  while IFS= read -r -d '' f; do files+=("$f"); done < <(git ls-files -z)
fi
if [ ${#files[@]} -eq 0 ]; then
  exit 0
fi

go_files=()
sh_files=()
workflow_files=()
md_files=()
for f in "${files[@]}"; do
  case $f in
    *.go) go_files+=("$f") ;;
    *.sh | scripts/hooks/*) sh_files+=("$f") ;;
    .github/workflows/*.yml | .github/workflows/*.yaml) workflow_files+=("$f") ;;
    *.md) md_files+=("$f") ;;
  esac
done

# Staged files are checked as the commit will hold them, not as the working
# tree has them: check them, and the configuration they are checked with,
# out of the index into a scratch directory.
if [ "$mode" = staged ]; then
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  {
    printf '%s\0' "${files[@]}"
    for config in .markdownlint-cli2.yaml .markdownlint.yaml .github/actionlint.yaml; do
      case " ${files[*]} " in
        *" $config "*) ;;
        *)
          # A configuration file that isn't tracked yet is left out; the
          # tool then runs with its defaults.
          if git ls-files --error-unmatch -- "$config" >/dev/null 2>&1; then
            printf '%s\0' "$config"
          fi
          ;;
      esac
    done
  } | git checkout-index -z --stdin --prefix="$work/"
  cd "$work"
fi

# run_on prints the output of a tool run on the files after "--", and
# returns its exit status. xargs keeps the command line under the
# platform's limit.
run_on() {
  local cmd=()
  while [ "$1" != "--" ]; do
    cmd+=("$1")
    shift
  done
  shift
  printf '%s\0' "$@" | xargs -0 "${cmd[@]}" 2>&1
}

if enabled gofmt && [ ${#go_files[@]} -gt 0 ]; then
  if have gofmt; then
    # golangci-lint's gofmt simplifies by default, as gofmt -s does.
    if out=$(run_on gofmt -s -l -- "${go_files[@]}") && [ -z "$out" ]; then
      pass "gofmt (${#go_files[@]} files)"
    else
      fail "gofmt: format these with gofmt -s -w <file>:"
      printf '%s\n' "$out" | indent
    fi
  else
    skip "gofmt: not installed; it comes with Go (https://go.dev/dl/)"
  fi
fi

if enabled goimports && [ ${#go_files[@]} -gt 0 ]; then
  if have goimports; then
    # -format-only checks the grouping without resolving missing imports,
    # which would load the module.
    if out=$(run_on goimports -format-only -local "$LOCAL_PREFIX" -l -- "${go_files[@]}") && [ -z "$out" ]; then
      pass "goimports (${#go_files[@]} files)"
    else
      fail "goimports: group the imports with goimports -w -local $LOCAL_PREFIX <file>:"
      printf '%s\n' "$out" | indent
    fi
  else
    skip "goimports: not installed; go install golang.org/x/tools/cmd/goimports@latest (builds goimports only)"
  fi
fi

if enabled headers && [ ${#go_files[@]} -gt 0 ]; then
  # Every Go file carries the licence line (scripts/check-headers.sh, which
  # the tests workflow runs too); staged files are checked in the index.
  if [ "$mode" = staged ]; then
    out=$(run_on bash "$root/scripts/check-headers.sh" --cached -- "${go_files[@]}") && status_headers=0 || status_headers=$?
  else
    out=$(bash "$root/scripts/check-headers.sh" 2>&1) && status_headers=0 || status_headers=$?
  fi
  # REUSE-IgnoreStart: the licence lines below are checked for, not this
  # file's own.
  # A new Go file also starts with the copyright line, right above it.
  bad=()
  for f in ${added[@]+"${added[@]}"}; do
    first=$(sed -n 1p "$f")
    second=$(sed -n 2p "$f")
    if ! [[ $first =~ ^//\ Copyright\ [0-9]{4}\ .+ ]] || [ "$second" != "// SPDX-License-Identifier: AGPL-3.0-only" ]; then
      bad+=("$f")
    fi
  done
  if [ "$status_headers" -eq 0 ] && [ ${#bad[@]} -eq 0 ]; then
    pass "headers (${#go_files[@]} files)"
  else
    fail "headers: a Go file starts with these two lines (see docs/development.md#licence-headers):"
    printf '%s\n' "// Copyright $(date +%Y) <your name>" "// SPDX-License-Identifier: AGPL-3.0-only" | indent
    # REUSE-IgnoreEnd
    if [ "$status_headers" -ne 0 ]; then
      printf '%s\n' "$out" | indent
    fi
    if [ ${#bad[@]} -gt 0 ]; then
      printf 'not at the top of: %s\n' "${bad[@]}" | indent
    fi
  fi
fi

if enabled names; then
  # scripts/check-names.sh, which the tests workflow runs too, on the
  # staged files in the index or on every tracked file.
  if [ "$mode" = staged ]; then
    out=$(run_on bash "$root/scripts/check-names.sh" --cached -- "${files[@]}") && status_names=0 || status_names=$?
  else
    out=$(bash "$root/scripts/check-names.sh" 2>&1) && status_names=0 || status_names=$?
  fi
  if [ "$status_names" -eq 0 ]; then
    pass "names (${#files[@]} files)"
  else
    fail "names: remove these, or list the file in .github/names-allowlist.txt with the reason:"
    printf '%s\n' "$out" | indent
  fi
fi

if enabled shellcheck && [ ${#sh_files[@]} -gt 0 ]; then
  if have shellcheck; then
    if out=$(run_on shellcheck -- "${sh_files[@]}"); then
      pass "shellcheck (${#sh_files[@]} files)"
    else
      fail "shellcheck:"
      printf '%s\n' "$out" | indent
    fi
  else
    skip "shellcheck: not installed; https://github.com/koalaman/shellcheck#installing"
  fi
fi

if enabled actionlint && [ ${#workflow_files[@]} -gt 0 ]; then
  if have actionlint; then
    # actionlint runs shellcheck on each run: script when shellcheck is
    # installed. No workflow runs Python, so pyflakes is off.
    args=(-no-color -oneline -pyflakes=)
    if [ -f .github/actionlint.yaml ]; then
      args+=(-config-file .github/actionlint.yaml)
    fi
    if out=$(run_on actionlint "${args[@]}" -- "${workflow_files[@]}"); then
      pass "actionlint (${#workflow_files[@]} files)"
    else
      fail "actionlint:"
      printf '%s\n' "$out" | indent
    fi
  else
    skip "actionlint: not installed; https://github.com/rhysd/actionlint/blob/main/docs/install.md"
  fi
fi

if enabled markdownlint && [ ${#md_files[@]} -gt 0 ]; then
  markdownlint=()
  if have markdownlint-cli2; then
    markdownlint=(markdownlint-cli2)
  elif have npx; then
    markdownlint=(npx --yes "markdownlint-cli2@$MARKDOWNLINT_CLI2_VERSION")
  fi
  if [ ${#markdownlint[@]} -gt 0 ]; then
    # .markdownlint.yaml sets the rules, .markdownlint-cli2.yaml the files
    # to leave out.
    if out=$(run_on "${markdownlint[@]}" -- "${md_files[@]}"); then
      pass "markdownlint (${#md_files[@]} files)"
    else
      fail "markdownlint:"
      printf '%s\n' "$out" | grep -vE '^(markdownlint-cli2 v|Finding:|Linting:|Summary:)' | indent
    fi
  else
    skip "markdownlint: needs Node.js (npx) or markdownlint-cli2 on PATH; https://nodejs.org/"
  fi
fi

exit $status
