#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# go-modules-refresh.sh updates each direct module of go.mod to its newest
# release that is at least COOLDOWN_DAYS old (default 7), the cooldown
# .github/dependabot.yml gives Go modules, and tidies go.mod. It does what
# Dependabot's grouped Go update did before it outgrew a job's time limit.
# It keeps to a module's major version (the path names it), and leaves
# alone modules at a pseudo-version or a +incompatible release, and
# pre-releases, but for aztft and azlist, which it moves to the head of
# their main branch (they have no releases to follow). It prints a Markdown table of what it updated, and exits 3
# if it updated nothing.
set -euo pipefail

cooldown=${COOLDOWN_DAYS:-7}
proxy=${GOPROXY_URL:-https://proxy.golang.org}
cutoff=$(date -u -d "-$cooldown days" +%s)

# escape writes a module path as the proxy wants it: an upper-case letter
# as ! and the letter in lower case.
escape() {
  sed -E 's/([A-Z])/!\L\1/g' <<<"$1"
}

# newer writes the releases of module newer than current, newest first.
newer() {
  local module=$1 current=$2
  curl -sSf --retry 3 "$proxy/$(escape "$module")/@v/list" |
    grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' |
    { cat; echo "$current"; } |
    sort -uVr |
    awk -v current="$current" '$0 == current { exit } { print }'
}

# released writes when release of module was published, in seconds.
released() {
  local time
  time=$(curl -sSf --retry 3 "$proxy/$(escape "$1")/@v/$2.info" | jq -r .Time)
  if [ -z "$time" ] || [ "$time" = null ]; then
    echo "no release time for $1@$2" >&2
    exit 1
  fi
  date -u -d "$time" +%s
}

updated=()
while read -r module current; do
  if ! [[ "$current" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    continue
  fi
  releases=$(newer "$module" "$current")
  for release in $releases; do
    at=$(released "$module" "$release")
    if [ "$at" -le "$cutoff" ]; then
      updated+=("| \`$module\` | $current | $release |")
      go get "$module@$release" >/dev/null
      break
    fi
  done
done < <(go list -m -f '{{if not (or .Main .Indirect)}}{{.Path}} {{.Version}}{{end}}' all)

# aztft and azlist, which map and list Azure resources, have no release
# that follows their main branch (azlist has no tags, aztft's last is from
# 2022), so go.mod pins them at pseudo-versions of main, which the loop
# above leaves alone. Move each to main's head once its commit is at least
# COOLDOWN_DAYS old.
for module in github.com/magodo/aztft github.com/magodo/azlist; do
  current=$(go list -m -f '{{.Version}}' "$module")
  info=$(curl -sSf --retry 3 "$proxy/$(escape "$module")/@v/main.info")
  head=$(jq -r .Version <<<"$info")
  at=$(date -u -d "$(jq -r .Time <<<"$info")" +%s)
  if [ "$head" != "$current" ] && [ "$at" -le "$cutoff" ]; then
    updated+=("| \`$module\` | $current | $head |")
    go get "$module@$head" >/dev/null
  fi
done

if [ ${#updated[@]} -eq 0 ]; then
  echo "Every direct module is at its newest release at least $cooldown days old." >&2
  exit 3
fi
go mod tidy
echo "| Module | From | To |"
echo "| --- | --- | --- |"
printf '%s\n' "${updated[@]}"
