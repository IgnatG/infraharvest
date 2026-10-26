#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Summarises, as Markdown on standard output, how the schema metadata in
# <new> differs from the metadata in <old>: for each provider's gzipped
# file (<provider>.json.gz), the release before and after, and the resource
# types added, removed and changed. The files hold one type to a line (see
# internal/catalogue/schemameta/gen), so a type's line is what changes.
# Gzip hides that from a diff: this is what a reviewer reads instead.
#
# It exits 0 if anything but the gzip bytes differs, 1 if nothing does, and
# above 1 on errors.
#
# Usage: schema-metadata-summary.sh <old-dir> <new-dir>
set -euo pipefail

old=${1:?usage: $0 <old-dir> <new-dir>}
new=${2:?usage: $0 <old-dir> <new-dir>}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# types <file.json.gz> <out>: one "type<TAB>line" per resource type, sorted.
types() {
  if [ -f "$1" ]; then
    gzip -dc "$1" | awk 'NR > 1 && /^"/ { line = $0; sub(/,$/, "", line); key = line; sub(/":.*/, "", key); sub(/^"/, "", key); print key "\t" line }' | sort
  fi > "$2"
}

# release <file.json.gz>: the release the file records, or "none".
release() {
  if [ -f "$1" ]; then
    # sed reads to the end: head would stop gzip with SIGPIPE, which
    # pipefail reports.
    gzip -dc "$1" | sed -n '1s/.*"version":"\([^"]*\)".*/\1/p'
  else
    echo none
  fi
}

# list <file>: up to 30 names, as code, comma-separated.
list() {
  local n names
  n=$(wc -l < "$1")
  # The backticks are Markdown.
  # shellcheck disable=SC2016
  names=$(head -n 30 "$1" | sed 's/.*/`&`/' | paste -sd, - | sed 's/,/, /g')
  if [ "$n" -gt 30 ]; then
    names="$names and $((n - 30)) more"
  fi
  echo "$names"
}

changed=1
echo "| Provider | Release | Types added | Removed | Changed |"
echo "|---|---|---|---|---|"
details=$work/details.md
: > "$details"
for path in "$new"/*.json.gz; do
  file=$(basename "$path")
  provider=${file%.json.gz}
  types "$old/$file" "$work/old"
  types "$new/$file" "$work/new"
  cut -f1 "$work/old" > "$work/old.names"
  cut -f1 "$work/new" > "$work/new.names"
  comm -13 "$work/old.names" "$work/new.names" > "$work/added"
  comm -23 "$work/old.names" "$work/new.names" > "$work/removed"
  # Types in both whose lines differ.
  join -t "$(printf '\t')" "$work/old" "$work/new" | awk -F '\t' '$2 != $3 { print $1 }' > "$work/changed"
  before=$(release "$old/$file")
  after=$(release "$new/$file")
  echo "| $provider | $before → $after | $(wc -l < "$work/added") | $(wc -l < "$work/removed") | $(wc -l < "$work/changed") |"
  if [ "$before" != "$after" ] || [ -s "$work/added" ] || [ -s "$work/removed" ] || [ -s "$work/changed" ]; then
    changed=0
  fi
  for kind in added removed changed; do
    if [ -s "$work/$kind" ]; then
      echo "- $provider, $kind: $(list "$work/$kind")" >> "$details"
    fi
  done
done
if [ -s "$details" ]; then
  echo
  cat "$details"
fi
exit "$changed"
