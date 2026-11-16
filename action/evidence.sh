#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Writes and signs the evidence of an import (the Sign the evidence step of
# action.yml, with sign-evidence true): the documents infraharvest export
# writes (inventory, run, gate and, where the release has it, graph) in
# <out>/_reports/evidence/, a SHA256SUMS file of them and of the report
# (report.json, report.md), and SHA256SUMS.sigstore.json, a Sigstore
# bundle that signs SHA256SUMS with the job's GitHub Actions identity,
# keylessly, as the release workflow signs a release's checksums. Anyone
# can then check, from the repository alone, which workflow wrote the
# report and the documents, and that they are as it wrote them:
#
#   cosign verify-blob --bundle SHA256SUMS.sigstore.json \
#     --certificate-identity-regexp '^https://github.com/<owner>/<repo>/' \
#     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
#     SHA256SUMS && sha256sum --check SHA256SUMS
#
# The inventory leaves out tags (export --drop-tags): the evidence is
# committed, and the tags of what the import didn't take over aren't the
# repository's. The step needs the id-token: write permission, and writes
# evidence-dir to $GITHUB_OUTPUT.
#
# Usage: evidence.sh
#
# Inputs, as variables: BINARY, OUT, STARTED_AT and FINISHED_AT (when the
# import started and finished, RFC 3339, from the Import step; either may
# be empty), and the runner's ACTIONS_ID_TOKEN_REQUEST_URL. COSIGN is the
# cosign to run (default cosign), for tests.
set -euo pipefail

# shellcheck source=action/output.sh
source "$(dirname "${BASH_SOURCE[0]}")/output.sh"

fail() {
  annotation error "evidence: $1"
  exit 1
}

bin="${BINARY:-infraharvest}"
cosign="${COSIGN:-cosign}"
out="${OUT:-infrastructure}"
reports="$out/_reports"
dir="$reports/evidence"

if [[ -z "${ACTIONS_ID_TOKEN_REQUEST_URL-}" ]]; then
  fail "signing needs the job's OIDC token: give the job the permission id-token: write"
fi
for file in "$reports/report.json" "$reports/report.md"; do
  if ! unlinked "$file"; then
    fail "$file isn't a regular file that no symbolic link leads to: nothing to sign"
  fi
done
# What the action writes must stay in the repository (see linked).
if linked "$dir" || [[ -e "$dir" && ! -d "$dir" ]]; then
  fail "$dir is a symbolic link, is in one, or isn't a directory; remove it"
fi
# The evidence of this run only: an earlier run's documents would be
# signed as this one's.
rm -rf "$dir"
mkdir -p "$dir"

times=()
[[ -n "${STARTED_AT-}" ]] && times+=("--started-at=$STARTED_AT")
[[ -n "${FINISHED_AT-}" ]] && times+=("--finished-at=$FINISHED_AT")

files=()
for doc in inventory run gate graph; do
  flags=()
  case "$doc" in
    inventory) flags=(--drop-tags ${times[@]+"${times[@]}"}) ;;
    run | graph) flags=(${times[@]+"${times[@]}"}) ;;
  esac
  if ! err=$("$bin" export "$out" "--schema=$doc" "--out-file=$dir/$doc.json" ${flags[@]+"${flags[@]}"} 2>&1 > /dev/null); then
    # A release from before the graph document: sign the others.
    if [[ "$doc" == graph && "$err" == *"unknown document"* ]]; then
      rm -f "$dir/$doc.json"
      continue
    fi
    fail "infraharvest export ($doc) failed: $err"
  fi
  files+=("$doc.json")
done

# Paths relative to the evidence directory, so that the check runs there.
(
  cd "$dir"
  if command -v sha256sum > /dev/null; then sum=(sha256sum); else sum=(shasum -a 256); fi
  "${sum[@]}" ../report.json ../report.md "${files[@]}" > SHA256SUMS
)

if ! err=$("$cosign" sign-blob --yes --bundle "$dir/SHA256SUMS.sigstore.json" "$dir/SHA256SUMS" 2>&1 > /dev/null); then
  rm -rf "$dir"
  fail "cosign sign-blob failed: $err"
fi
annotation notice "evidence: signed the report and ${#files[@]} documents in $dir"
output evidence-dir "$dir"
