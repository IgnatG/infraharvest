#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Writes and signs the evidence of an import (the Sign the evidence step of
# action.yml, with sign-evidence true), in <out>/_reports/evidence/: the
# documents infraharvest export writes (inventory, run, gate and, where the
# release has it, graph), a copy of the report (report.json, report.md), a
# manifest.json that says which repository, commit and workflow run wrote
# them, a SHA256SUMS file of all of these, and SHA256SUMS.sigstore.json, a
# Sigstore bundle that signs SHA256SUMS with the job's GitHub Actions
# identity, keylessly, as the release workflow signs a release's checksums.
# Anyone can then check, from the repository alone, which workflow wrote the
# report and the documents, and that they are as it wrote them:
#
#   cosign verify-blob --bundle SHA256SUMS.sigstore.json \
#     --certificate-identity-regexp '^https://github.com/<owner>/<repo>/' \
#     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
#     SHA256SUMS && sha256sum --check SHA256SUMS
#
# The directory is flat: SHA256SUMS lists every file by its bare name, with no
# path, and manifest.json among them. The manifest has the schema
# infraweaver/evidence-manifest/v1, the kind (import), the repository
# (GITHUB_REPOSITORY), the run_id of the run document, the workflow_run_id
# (GITHUB_RUN_ID), the commit (GITHUB_SHA, which is the commit the signing
# certificate names), created_at (the import's finish, else now) and, for every
# file but itself, its name, its role (report or document) and its size. The
# control plane's Control plane step uploads the directory as it is
# (control-plane.sh).
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
# be empty), the runner's ACTIONS_ID_TOKEN_REQUEST_URL, GITHUB_REPOSITORY,
# GITHUB_SHA and GITHUB_RUN_ID. COSIGN is the cosign to run (default cosign),
# for tests.
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
command -v jq > /dev/null || fail "jq is needed to write the manifest and isn't installed"
# What the manifest says about the run, as the control plane reads it: the
# commit is the one the certificate will name, so it must be the job's own.
repository="${GITHUB_REPOSITORY-}"
commit="${GITHUB_SHA-}"
workflow_run="${GITHUB_RUN_ID-}"
[[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "GITHUB_REPOSITORY must be owner/name, got '$repository'"
[[ "$commit" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]] || fail "GITHUB_SHA must be the 40 hex digits of the commit"
[[ "$workflow_run" =~ ^[0-9]+$ ]] || fail "GITHUB_RUN_ID must be the number of the workflow run"
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

# The report is copied in, so that the directory is flat and holds all it
# signs.
cp "$reports/report.json" "$reports/report.md" "$dir/"
files=(report.json report.md)
docs=()
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
    rm -rf "$dir"
    fail "infraharvest export ($doc) failed: $err"
  fi
  files+=("$doc.json")
  docs+=("$doc.json")
done

# The run document's run_id is the engine's: the manifest names it.
run_id=$(jq -r '.run_id // empty' "$dir/run.json") || run_id=
if [[ ! "$run_id" =~ ^[A-Za-z0-9._:-]{1,128}$ ]]; then
  rm -rf "$dir"
  fail "the run document has no run_id the manifest can name"
fi
created_at="${FINISHED_AT-}"
if [[ ! "$created_at" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$ ]]; then
  created_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fi
listed=$(
  for name in "${files[@]}"; do
    role=document
    case "$name" in report.json | report.md) role=report ;; esac
    bytes=$(wc -c < "$dir/$name")
    jq -n --arg name "$name" --arg role "$role" --argjson bytes "${bytes//[[:space:]]/}" '{name: $name, role: $role, bytes: $bytes}'
  done | jq -s '.'
)
jq -n --arg kind import --arg repository "$repository" --arg run_id "$run_id" \
  --arg workflow_run_id "$workflow_run" --arg commit "$commit" --arg created_at "$created_at" \
  --argjson files "$listed" \
  '{schema: "infraweaver/evidence-manifest/v1", kind: $kind, repository: $repository, run_id: $run_id,
    workflow_run_id: $workflow_run_id, commit: $commit, created_at: $created_at, files: $files}' > "$dir/manifest.json"

# Bare names, so that the check runs in the evidence directory and the
# control plane takes the list as it is.
(
  cd "$dir"
  if command -v sha256sum > /dev/null; then sum=(sha256sum); else sum=(shasum -a 256); fi
  "${sum[@]}" "${files[@]}" manifest.json > SHA256SUMS
)

# The bundle is the protobuf-JSON Sigstore bundle (a message signature over
# the bytes of SHA256SUMS), not cosign 2's legacy {base64Signature, cert,
# rekorBundle} file.
if ! err=$("$cosign" sign-blob --yes --new-bundle-format --bundle "$dir/SHA256SUMS.sigstore.json" "$dir/SHA256SUMS" 2>&1 > /dev/null); then
  rm -rf "$dir"
  fail "cosign sign-blob failed: $err"
fi
if ! jq -e 'type == "object" and (.messageSignature | type) == "object" and (has("dsseEnvelope") | not)' "$dir/SHA256SUMS.sigstore.json" > /dev/null 2>&1; then
  rm -rf "$dir"
  fail "cosign wrote no message-signature bundle in the new format: use cosign 2.4 or later with --new-bundle-format, or cosign 3"
fi
annotation notice "evidence: signed the report and ${#docs[@]} documents in $dir"
output evidence-dir "$dir"
