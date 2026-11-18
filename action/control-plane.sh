#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Sends the metadata of a run to a control plane (the Control plane step of
# action.yml, with control-plane-url set). It first trades the job's GitHub
# Actions OIDC token, for the audience AUDIENCE (default infraharvest-api),
# for a run token: POST <url>/api/v1/session with an empty body and the OIDC
# token in the header X-GitHub-OIDC-Token, answered with JSON,
# {"token": "...", "expires_at": "..."}. Then it runs infraharvest export for
# the inventory, run, gate and graph documents, and POSTs each to
# <url>/api/v1/ingest/{inventory|run|gate|graph} as application/json,
# gzip-compressed (Content-Encoding: gzip), with the run token as a bearer
# token and the header Idempotency-Key: <run_id>:<document>. The OIDC token
# goes to the session endpoint only. Nothing else is sent. See
# docs/export.md.
#
# Usage: control-plane.sh check|upload
#
# check validates the inputs, before anything long has run. upload exports
# and sends; it writes control-plane-run-id (the run_id of the run document)
# to $GITHUB_OUTPUT once all four documents were accepted (a 2xx).
#
# The URL must be https, or http for localhost. The tokens are masked and
# never printed, and go to curl in a file, not in its arguments. A 5xx, a
# 429 and a connection failure are retried with a growing delay; any other
# answer fails at once, with the reason the control plane gives in its JSON
# body, {"error": "..."}. A failure fails the step, unless OPTIONAL is true.
#
# Inputs, as variables: URL, AUDIENCE, DROP_TAGS, DROP_TAG, HASH_IDS,
# HASH_KEY (else INFRAHARVEST_EXPORT_KEY), OPTIONAL; and, for upload, BINARY,
# OUT, IMPORT, IMPORT_OUTCOME, STARTED_AT and FINISHED_AT (when the import
# started and finished, RFC 3339, from the Import step; either may be
# empty), and the runner's ACTIONS_ID_TOKEN_REQUEST_URL and
# ACTIONS_ID_TOKEN_REQUEST_TOKEN. INFRAHARVEST_UPLOAD_RETRY_DELAY is the
# first delay in seconds between attempts (default 2, doubling), for tests.
set -euo pipefail

# shellcheck source=action/output.sh
source "$(dirname "${BASH_SOURCE[0]}")/output.sh"

attempts=4

url="${URL-}"
audience="${AUDIENCE:-infraharvest-api}"
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
  # The audience goes into the query of the token request as it is.
  if [[ ! "$audience" =~ ^[A-Za-z0-9._~:/-]+$ ]]; then
    fail "control-plane-audience may hold letters, digits and . _ ~ : / - only, got '$audience'"
  fi
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

# http_request <url> <header-file> <out-file> [curl-argument...]: a GET, or
# whatever the extra curl arguments make of it (-X POST and a body), with
# the headers of the file. A 5xx, a 429 and a connection failure are
# retried. Sets http_code, and leaves the answer's body in out-file. Returns
# 0 on a 2xx.
http_request() {
  local target=$1 headers=$2 body=$3
  shift 3
  local args=(--silent --show-error --fail-with-body --connect-timeout 10 --max-time 60
    --max-redirs 0 -H "@$headers" -o "$body" -w '%{http_code}' "$@")
  if [[ "$target" == https://* ]]; then args+=(--proto '=https'); else args+=(--proto '=http'); fi
  local attempt=1 delay=${INFRAHARVEST_UPLOAD_RETRY_DELAY-2} rc
  while :; do
    rc=0
    http_code=$(curl "${args[@]}" "$target" 2> "$tmp/curl-error") || rc=$?
    http_code=${http_code:-000}
    if [[ "$http_code" =~ ^2[0-9][0-9]$ ]]; then return 0; fi
    # 000: curl got no answer, such as a refused connection or a timeout.
    # Any other 4xx is a refusal, which another try wouldn't change.
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

# describe <body-file> <what>: what went wrong with the last request, on one
# line: the status, curl's own error, who refused what, and the reason in the
# answer's JSON body ({"error": "..."}), else the start of the body. what is
# what the request was, such as "upload".
describe() {
  local body=$1 what=$2 msg="HTTP $http_code" reason=
  if [[ -s "$tmp/curl-error" ]]; then msg+=": $(head -c 300 "$tmp/curl-error" | tr '\r\n' '  ')"; fi
  case "$http_code" in
    402) msg+=": the organisation's plan refused the $what, such as for being over its cloud-account limit; this is not a problem with the token" ;;
    401 | 403) msg+=": the control plane refused the token" ;;
  esac
  if [[ -s "$body" ]]; then
    reason=$(jq -r 'if type == "object" and (.error | type) == "string" then .error else empty end' "$body" 2> /dev/null | head -c 300 | tr '\r\n' '  ') || reason=
    if [[ -z "$reason" ]]; then reason=$(head -c 300 "$body" | tr '\r\n' '  '); fi
  fi
  if [[ -n "$reason" ]]; then msg+=": $reason"; fi
  printf '%s' "$msg"
}

# request_token: sets token to the job's OIDC token for the audience, and
# masks it. Not run in a subshell: problem exits the script.
request_token() {
  if [[ -z "${ACTIONS_ID_TOKEN_REQUEST_URL-}" || -z "${ACTIONS_ID_TOKEN_REQUEST_TOKEN-}" ]]; then
    problem "no GitHub Actions OIDC token can be requested: set 'id-token: write' in the workflow's permissions"
  fi
  local sep='?'
  [[ "$ACTIONS_ID_TOKEN_REQUEST_URL" == *\?* ]] && sep='&'
  ( umask 077; printf 'Authorization: Bearer %s\n' "$ACTIONS_ID_TOKEN_REQUEST_TOKEN" > "$tmp/oidc-request-headers" )
  local target="${ACTIONS_ID_TOKEN_REQUEST_URL}${sep}audience=${audience}"
  if ! http_request "$target" "$tmp/oidc-request-headers" "$tmp/oidc-response"; then
    problem "couldn't request a GitHub Actions OIDC token for the audience $audience ($(describe "$tmp/oidc-response" "request")); is 'id-token: write' set?"
  fi
  token=$(jq -r '.value // empty' "$tmp/oidc-response" 2> /dev/null) || token=
  rm -f "$tmp/oidc-response" "$tmp/oidc-request-headers"
  if [[ ! "$token" =~ ^[A-Za-z0-9._-]+$ ]]; then
    problem "GitHub returned no usable OIDC token for the audience $audience"
  fi
  printf '::add-mask::%s\n' "$token"
}

# open_session: trades the OIDC token for the control plane's run token: sets
# run_token, masks it, and leaves the header file the uploads send it in.
# The OIDC token goes to the session endpoint only.
open_session() {
  local expires
  request_token
  ( umask 077; printf 'X-GitHub-OIDC-Token: %s\n' "$token" > "$tmp/session-headers" )
  if ! http_request "$base/api/v1/session" "$tmp/session-headers" "$tmp/session-response" \
    -X POST --data-binary '' -H 'Content-Type:'; then
    problem "opening a session at $base/api/v1/session failed: $(describe "$tmp/session-response" "session")"
  fi
  run_token=$(jq -r 'if type == "object" and (.token | type) == "string" then .token else empty end' "$tmp/session-response" 2> /dev/null) || run_token=
  expires=$(jq -r 'if type == "object" and (.expires_at | type) == "string" then .expires_at else empty end' "$tmp/session-response" 2> /dev/null | head -c 64 | tr -cd '[:graph:]') || expires=
  rm -f "$tmp/session-response" "$tmp/session-headers"
  if [[ ! "$run_token" =~ ^[[:graph:]]+$ ]]; then
    problem "the session answer at $base/api/v1/session has no usable token"
  fi
  printf '::add-mask::%s\n' "$run_token"
  ( umask 077; printf 'Authorization: Bearer %s\n' "$run_token" > "$tmp/headers" )
  annotation notice "control plane: opened a session${expires:+ (the token expires at $expires)}"
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

  command -v jq > /dev/null || problem "jq is needed to read the answers and the run's ID and isn't installed"
  command -v gzip > /dev/null || problem "gzip is needed to compress the documents and isn't installed"

  # The session comes first, so that a refusal (a token the control plane
  # doesn't take, a plan that doesn't allow the upload) shows before the
  # export has run.
  open_session

  # The redaction flags apply to the inventory, which has the tags, IDs and
  # names, and --hash-ids to the graph too, which has the IDs.
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

  # The report holds no times: the import's own start and finish go to the
  # run (started_at, finished_at) and the inventory (generated_at is the
  # finish), so that sending the same run again changes neither.
  local times=()
  [[ -n "${STARTED_AT-}" ]] && times+=("--started-at=$STARTED_AT")
  [[ -n "${FINISHED_AT-}" ]] && times+=("--finished-at=$FINISHED_AT")

  local doc
  for doc in inventory run gate graph; do
    flags=()
    if [[ "$doc" == inventory ]]; then flags=(${redact[@]+"${redact[@]}"}); fi
    if [[ "$doc" == graph && "$hash_ids" == true ]]; then flags=(--hash-ids); fi
    if [[ "$doc" == inventory || "$doc" == run ]]; then flags+=(${times[@]+"${times[@]}"}); fi
    if ! err=$(INFRAHARVEST_EXPORT_KEY="$key" "$bin" export "$out" "--schema=$doc" "--out-file=$tmp/$doc.json" ${flags[@]+"${flags[@]}"} 2>&1 > /dev/null); then
      problem "infraharvest export ($doc) failed: $err"
    fi
    if ! gzip -n -c "$tmp/$doc.json" > "$tmp/$doc.json.gz"; then
      problem "couldn't compress the $doc document"
    fi
  done

  run_id=$(jq -r '.run_id // empty' "$tmp/run.json")
  [[ -n "$run_id" ]] || problem "the run document has no run_id"
  # The run_id goes into a header.
  [[ "$run_id" =~ ^[A-Za-z0-9._:-]+$ ]] || problem "the run document's run_id has characters that can't go in a header"

  for doc in inventory run gate graph; do
    if ! http_request "$base/api/v1/ingest/$doc" "$tmp/headers" "$tmp/response" \
      -X POST -H 'Content-Type: application/json' -H 'Content-Encoding: gzip' \
      -H "Idempotency-Key: $run_id:$doc" --data-binary "@$tmp/$doc.json.gz"; then
      problem "sending the $doc document to $base/api/v1/ingest/$doc failed: $(describe "$tmp/response" "upload")"
    fi
    annotation notice "control plane: sent the $doc document (HTTP $http_code)"
  done
  output control-plane-run-id "$run_id"
}

base=
tmp=
token=
run_token=
http_code=
case "${1-}" in
  check) check ;;
  upload) upload ;;
  *)
    echo "usage: control-plane.sh check|upload" >&2
    exit 2
    ;;
esac
