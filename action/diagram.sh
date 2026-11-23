#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Draws the imported estate as a Mermaid diagram (the Keep the earlier graph
# and Draw the diagram steps of action.yml, with diagram true): infraharvest
# export's graph document, drawn by the Terragram CLI, in the job summary and
# in the body of the pull request. Nothing here fails the job: the diagram is
# an extra, so whatever goes wrong is a warning and the run goes on without it.
#
# Usage: diagram.sh keep|draw
#
# keep runs before drift and the import, which can write the out directory:
# it copies the earlier graph, the one the evidence an earlier run signed has
# (<out>/_reports/evidence/graph.json, see evidence.sh), to $RUNNER_TEMP, and
# writes its path to $GITHUB_OUTPUT as path; nothing is written when there is
# none. With draw, that file is the base: the diagram then shows only what
# changed, with one reference of context (terragram graph --base --delta).
# Without one, it shows the whole estate.
#
# draw exports the graph document the way the Control plane step does, with
# the same redaction, and draws it. The earlier graph isn't redacted, and a
# diagram of what changed would show its IDs and addresses, so with
# control-plane-hash-ids or control-plane-drop-addresses the whole estate is
# drawn, from the redacted graph: nothing is in the diagram that the export
# wouldn't send. It writes the diagram, as Markdown, to
# $RUNNER_TEMP/infraharvest-diagram.md, adds it to the job summary and writes
# its path to $GITHUB_OUTPUT as diagram-path, for the pull request's body
# (pull-request-details.sh). A diagram too large for GitHub to draw is
# replaced by a line saying so.
#
# Inputs, as variables: OUT, HASH_IDS, DROP_ADDRESSES; for draw, BINARY, CLI
# (the Terragram CLI's .mjs file, which the install step's path output says;
# empty when it wasn't installed), BASE (keep's path), HASH_KEY (else
# INFRAHARVEST_EXPORT_KEY) and the runner's RUNNER_TEMP and GITHUB_STEP_SUMMARY.
# INFRAHARVEST_DIAGRAM_MAX is the most characters of a diagram to add (default
# 45000), for tests.
set -euo pipefail

# shellcheck source=action/output.sh
source "$(dirname "${BASH_SOURCE[0]}")/output.sh"

out="${OUT:-infrastructure}"
temp="${RUNNER_TEMP:?}"
hash_ids="${HASH_IDS-false}"
drop_addresses="${DROP_ADDRESSES-false}"
# What GitHub draws of a Mermaid diagram: its default limit is 50000
# characters of text.
max=${INFRAHARVEST_DIAGRAM_MAX-45000}

# skip <message>: the diagram is left out, with a warning, and the step
# succeeds.
skip() {
  annotation warning "diagram: $1"
  exit 0
}

keep() {
  local src="$out/_reports/evidence/graph.json" dest="$temp/infraharvest-graph-base.json"
  rm -f "$dest"
  if [[ "$hash_ids" == true || "$drop_addresses" == true ]]; then
    annotation notice "diagram: the export redacts IDs or addresses, which the earlier graph doesn't: drawing the whole estate, not what changed"
    return 0
  fi
  # Not through a symbolic link the repository commits (see unlinked in
  # output.sh).
  if unlinked "$src" && [[ -s "$src" ]]; then
    cp -- "$src" "$dest"
    output path "$dest"
  elif [[ -e "$src" || -L "$src" ]]; then
    annotation warning "diagram: $src isn't a regular file that no symbolic link leads to: drawing the whole estate, not what changed"
  else
    annotation notice "diagram: no earlier graph in $src: drawing the whole estate"
  fi
}

# render <title> [base]: terragram graph's Mermaid flowchart of the graph
# document, in $mermaid, with what changed since base when there is one.
# Returns terragram's exit code. What it logs, which can hold names from the
# cloud, goes to the log with no workflow commands.
render() {
  local args=("$cli" graph "$graph" -f mermaid --title "$1") code=0
  if [[ -n "${2-}" ]]; then args+=(--base "$2" --delta); fi
  commands_off
  { node "${args[@]}" > "$mermaid"; } 2>&1 || code=$?
  commands_on
  return "$code"
}

draw() {
  local fence=$'```' bin="${BINARY:-infraharvest}" key err flags=() size text delta=false code=0
  cli="${CLI-}"
  graph="$temp/infraharvest-graph.json"
  mermaid="$temp/infraharvest-diagram.mmd"
  [[ -n "$cli" && -f "$cli" ]] || skip "the Terragram CLI wasn't installed: no diagram"
  command -v node > /dev/null || skip "node isn't installed: no diagram"
  rm -f "$graph" "$mermaid" "$temp/infraharvest-diagram.md"

  # The Control plane step's flags for the graph.
  [[ "$hash_ids" == true ]] && flags+=(--hash-ids)
  [[ "$drop_addresses" == true ]] && flags+=(--drop-addresses)
  key="${HASH_KEY:-${INFRAHARVEST_EXPORT_KEY-}}"
  # Check inputs refuses a short key before the import; this says so if the
  # step is run on its own.
  if [[ "$hash_ids" == true ]] && short_hash_key "$key"; then
    skip "the hash key must have at least 16 bytes (control-plane-hash-key, or INFRAHARVEST_EXPORT_KEY): no diagram"
  fi
  if ! err=$(INFRAHARVEST_EXPORT_KEY="$key" "$bin" export "$out" --schema=graph "--out-file=$graph" ${flags[@]+"${flags[@]}"} 2>&1 > /dev/null); then
    # A release from before the graph document fails here, too.
    skip "infraharvest export (graph) failed: $err"
  fi

  # What changed, when there is an earlier graph; else, or when that fails,
  # as a graph from an older release can, the whole estate.
  if [[ -n "${BASE-}" && -f "$BASE" ]]; then
    if render "Changes to the imported estate" "$BASE"; then
      delta=true
    else
      annotation warning "diagram: couldn't draw what changed since the earlier graph: drawing the whole estate"
    fi
  fi
  if [[ "$delta" != true ]]; then
    render "Imported estate" || code=$?
    if [[ "$code" != 0 ]]; then skip "terragram graph exited with $code: no diagram"; fi
  fi
  if [[ ! -s "$mermaid" ]]; then skip "terragram graph wrote nothing: no diagram"; fi
  # A name with a code fence in it would end the diagram's block early.
  if grep -q "$fence" "$mermaid"; then skip "the diagram has a code fence in it: not adding it"; fi

  size=$(wc -c < "$mermaid")
  size=${size//[[:space:]]/}
  if ((size > max)); then
    text="_The diagram of the imported estate has $size characters, more than GitHub draws (about $max). Draw it with \`infraharvest export --schema graph\` and \`terragram graph\`._"
  else
    text=$(printf '%smermaid\n%s\n%s' "$fence" "$(cat "$mermaid")" "$fence")
  fi
  printf '## Diagram\n\n%s\n' "$text" > "$temp/infraharvest-diagram.md"
  rm -f "$graph" "$mermaid"
  cat "$temp/infraharvest-diagram.md" >> "${GITHUB_STEP_SUMMARY:?}"
  output diagram-path "$temp/infraharvest-diagram.md"
}

cli=
graph=
mermaid=
case "${1-}" in
  keep) keep ;;
  draw) draw ;;
  *)
    echo "usage: diagram.sh keep|draw" >&2
    exit 2
    ;;
esac
