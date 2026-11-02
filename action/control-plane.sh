#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Sends the metadata of a run to a control plane (the Control plane step of
# action.yml, with control-plane-url set). It runs infraharvest export for
# the inventory, run and gate documents, and POSTs each to
# <url>/api/v1/ingest/{inventory|run|gate} as application/json, with the
# job's GitHub Actions OIDC token for the audience infraharvest-api as a
# bearer token. Nothing else is sent. See docs/export.md.
#
# Usage: control-plane.sh check|upload
#
# check validates the inputs, before anything long has run. upload exports
# and sends; it writes control-plane-run-id (the run_id of the run document)
# to $GITHUB_OUTPUT once all three documents were accepted (a 2xx).
#
# The URL must be https, or http for localhost. The token is masked and
# never printed, and goes to curl in a file, not in its arguments. A 5xx, a
# 429 and a connection failure are retried with a growing delay; any other
# answer fails at once. A failure fails the step, unless OPTIONAL is true.
#
# Inputs, as variables: URL, DROP_TAGS, DROP_TAG, HASH_IDS, HASH_KEY (else
# INFRAHARVEST_EXPORT_KEY), OPTIONAL; and, for upload, BINARY, OUT, IMPORT,
# IMPORT_OUTCOME, and the runner's ACTIONS_ID_TOKEN_REQUEST_URL and
# ACTIONS_ID_TOKEN_REQUEST_TOKEN. INFRAHARVEST_UPLOAD_RETRY_DELAY is the
# first delay in seconds between attempts (default 2, doubling), for tests.
set -euo pipefail

# shellcheck source=action/output.sh
source "$(dirname "${BASH_SOURCE[0]}")/output.sh"

audience=infraharvest-api
attempts=4

url="${URL-}"
optional="${OPTIONAL-false}"
hash_ids="${HASH_IDS-false}"
drop_tags="${DROP_TAGS-false}"

# fail <message>: a mistake in the inputs, which optional doesn't excuse.
fail() {
  annotation error "$1"
  exit 1
}

# problem <message>: a failure of the export or the upload, which fails the
# step, unless the control plane is optional.
problem() {
  if [[ "$optional" == true ]]; then
    annotation warning "control plane: $1 (control-plane-optional is true: not failing the step)"
    exit 0
  fi
  annotation error "control plane: $1"
  exit 1
}

# set_base: sets base to url without trailing slashes, after checking it is https, or
# http for localhost, with no user, query or fragment.
set_base() {
  local u=$url
  while [[ "$u" == */ ]]; do u=${u%/}; done
  if [[ "$u" =~ ^https://[^/?#@[:space:]]+(/[^?#[:space:]]*)?$ ]]; then
    :
  elif [[ "$u" =~ ^http://(localhost|127\.0\.0\.1|\[::1\])(:[0-9]+)?(/[^?#[:space:]]*)?$ ]]; then
    :
  else
    fail "control-plane-url must be an https:// URL (http:// only for localhost) with no user, query or fragment, got '$url'"
  fi
  base=$u
}

check() {
  [[ -n "$url" ]] || return 0
  set_base
  local input
  for input in "control-plane-optional=$optional" "control-plane-hash-ids=$hash_ids" "control-plane-drop-tags=$drop_tags"; do
    case "${input#*=}" in
      true | false) ;;
      *) fail "${input%%=*} must be true or false, got '${input#*=}'" ;;
    esac
  done
  if [[ "${IMPORT-}" != true ]]; then
    fail "control-plane-url sends the metadata of an import: it needs import to be true"
  fi
  if [[ "$hash_ids" == true && -z "${HASH_KEY-}" && -z "${INFRAHARVEST_EXPORT_KEY-}" ]]; then
    fail "control-plane-hash-ids needs a key: set control-plane-hash-key, or INFRAHARVEST_EXPORT_KEY from a secret"
  fi
  if [[ "$hash_ids" != true && -n "${HASH_KEY-}" ]]; then
    fail "control-plane-hash-key takes effect with control-plane-hash-ids set to true"
  fi
  if [[ -z "${ACTIONS_ID_TOKEN_REQUEST_URL-}" || -z "${ACTIONS_ID_TOKEN_REQUEST_TOKEN-}" ]]; then
    if [[ "$optional" == true ]]; then
      annotation warning "control-plane-url needs a GitHub Actions OIDC token, which the job has no permission to request: set 'id-token: write' in the workflow's permissions; the upload will be skipped"
    else
      fail "control-plane-url needs a GitHub Actions OIDC token, which the job has no permission to request: set 'id-token: write' in the workflow's permissions"
    fi
  fi
}

# http_request <url> <header-file> <out-file> [data-file]: GET, or POST the
# data file as JSON, retrying a 5xx, a 429 and a connection failure. Sets
# http_code, and leaves the answer's body in out-file. Returns 0 on a 2xx.
http_request() {
  local target=$1 headers=$2 body=$3 data=${4-}
  local args=(--silent --show-error --fail-with-body --connect-timeout 10 --max-time 60
    --max-redirs 0 -H "@$headers" -o "$body" -w '%{http_code}')
  if [[ "$target" == https://* ]]; then args+=(--proto '=https'); else args+=(--proto '=http'); fi
  if [[ -n "$data" ]]; then
    args+=(-X POST -H 'Content-Type: application/json' --data-binary "@$data")
  fi
  local attempt=1 delay=${INFRAHARVEST_UPLOAD_RETRY_DELAY-2} rc
  while :; do
    rc=0
    http_code=$(curl "${args[@]}" "$target" 2> "$tmp/curl-error") || rc=$?
    http_code=${http_code:-000}
    if [[ "$http_code" =~ ^2[0-9][0-9]$ ]]; then return 0; fi
    # 000: curl got no answer, such as a refused connection or a timeout.
    if [[ "$http_code" == 000 || "$http_code" == 429 || "$http_code" =~ ^5[0-9][0-9]$ ]]; then
      if ((attempt < attempts)); then
        annotation notice "control plane: attempt $attempt got $http_code (curl exit $rc); trying again in ${delay}s"
        sleep "$delay"
        delay=$((delay * 2))
        attempt=$((attempt + 1))
        continue
      fi
    fi
    return 1
  done
}

# describe: what went wrong with the last request, on one line: the status,
# curl's own error, and the start of the answer's body.
describe() {
  local msg="HTTP $http_code" text
  if [[ -s "$tmp/curl-error" ]]; then msg+=": $(head -c 300 "$tmp/curl-error" | tr '\r\n' '  ')"; fi
  if [[ -s "$1" ]]; then
    text=$(head -c 300 "$1" | tr '\r\n' '  ')
    msg+=" $text"
  fi
  printf '%s' "$msg"
}

# request_token: sets token to the job's OIDC token for the audience, and
# masks it. Not run in a subshell: problem exits the script.
request_token() {
  if [[ -z "${ACTIONS_ID_TOKEN_REQUEST_URL-}" || -z "${ACTIONS_ID_TOKEN_REQUEST_TOKEN-}" ]]; then
    problem "no GitHub Actions OIDC token can be requested: set 'id-token: write' in the workflow's permissions"
  fi
  command -v jq > /dev/null || problem "jq is needed to read the OIDC token and isn't installed"
  local sep='?'
  [[ "$ACTIONS_ID_TOKEN_REQUEST_URL" == *\?* ]] && sep='&'
  ( umask 077; printf 'Authorization: Bearer %s\n' "$ACTIONS_ID_TOKEN_REQUEST_TOKEN" > "$tmp/oidc-request-headers" )
  local target="${ACTIONS_ID_TOKEN_REQUEST_URL}${sep}audience=${audience}"
  if ! http_request "$target" "$tmp/oidc-request-headers" "$tmp/oidc-response"; then
    problem "couldn't request a GitHub Actions OIDC token for the audience $audience ($(describe "$tmp/oidc-response")); is 'id-token: write' set?"
  fi
  token=$(jq -r '.value // empty' "$tmp/oidc-response" 2> /dev/null) || token=
  rm -f "$tmp/oidc-response" "$tmp/oidc-request-headers"
  if [[ ! "$token" =~ ^[A-Za-z0-9._-]+$ ]]; then
    problem "GitHub returned no usable OIDC token for the audience $audience"
  fi
  printf '::add-mask::%s\n' "$token"
}

upload() {
  [[ -n "$url" ]] || return 0
  local bin out report
  set_base
  bin="${BINARY:-infraharvest}"
  out="${OUT:-infrastructure}"
  report="$out/_reports/report.json"

  # The import can have failed before it wrote a report: nothing to send,
  # and the import's own failure is the one to show.
  if ! unlinked "$report"; then
    if [[ "${IMPORT_OUTCOME-}" == failure ]]; then
      annotation warning "control plane: the import left no report to send ($report)"
      return 0
    fi
    problem "no report to send: $report isn't a regular file that no symbolic link leads to"
  fi

  tmp=$(umask 077; mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/infraharvest-control-plane.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT

  # The redaction flags apply to the inventory, the only document with tags,
  # IDs and names.
  local redact=() flags=() key tag err
  [[ "$drop_tags" == true ]] && redact+=(--drop-tags)
  while IFS= read -r tag; do
    tag="${tag#"${tag%%[![:space:]]*}"}"
    tag="${tag%"${tag##*[![:space:]]}"}"
    [[ -z "$tag" ]] && continue
    redact+=("--drop-tag=$tag")
  done <<< "$(printf '%s' "${DROP_TAG-}" | tr ',' '\n')"
  if [[ "$hash_ids" == true ]]; then redact+=(--hash-ids); fi
  # The key reaches infraharvest as INFRAHARVEST_EXPORT_KEY, from the input
  # or the workflow's own variable.
  key="${HASH_KEY:-${INFRAHARVEST_EXPORT_KEY-}}"

  local doc
  for doc in inventory run gate; do
    flags=()
    if [[ "$doc" == inventory ]]; then flags=(${redact[@]+"${redact[@]}"}); fi
    if ! err=$(INFRAHARVEST_EXPORT_KEY="$key" "$bin" export "$out" "--schema=$doc" "--out-file=$tmp/$doc.json" ${flags[@]+"${flags[@]}"} 2>&1 > /dev/null); then
      problem "infraharvest export ($doc) failed: $err"
    fi
  done

  command -v jq > /dev/null || problem "jq is needed to read the run's ID and isn't installed"
  run_id=$(jq -r '.run_id // empty' "$tmp/run.json")
  [[ -n "$run_id" ]] || problem "the run document has no run_id"

  request_token
  ( umask 077; printf 'Authorization: Bearer %s\n' "$token" > "$tmp/headers" )

  for doc in inventory run gate; do
    if ! http_request "$base/api/v1/ingest/$doc" "$tmp/headers" "$tmp/response" "$tmp/$doc.json"; then
      problem "sending the $doc document to $base/api/v1/ingest/$doc failed: $(describe "$tmp/response")"
    fi
    annotation notice "control plane: sent the $doc document (HTTP $http_code)"
  done
  output control-plane-run-id "$run_id"
}

base=
tmp=
token=
http_code=
case "${1-}" in
  check) check ;;
  upload) upload ;;
  *)
    echo "usage: control-plane.sh check|upload" >&2
    exit 2
    ;;
esac
