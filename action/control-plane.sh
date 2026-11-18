#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Sends the metadata of a run to a control plane (the Control plane step of
# action.yml, with control-plane-url set). It first trades the job's GitHub
# Actions OIDC token, for the audience AUDIENCE (default infraharvest-api),
# for a run token: POST <url>/api/v1/session with an empty body and the OIDC
# token in the header X-GitHub-OIDC-Token, answered with JSON,
# {"token": "...", "expires_at": "..."}. Then it runs infraharvest export for
# the documents of the run, and POSTs each to <url>/api/v1/ingest/<document>
# as application/json, gzip-compressed (Content-Encoding: gzip), with the run
# token as a bearer token and the header Idempotency-Key: <run_id>:<document>.
# The OIDC token goes to the session endpoint only. Nothing else is sent. See
# docs/export.md.
#
# The documents are those of what ran: an import sends ocsf-inventory, run,
# gate and graph; a drift check sends drift-summary, and, when no import ran,
# the run document of the drift check (export --command drift) before it; both
# send the five. The inventory is sent as the OCSF inventory: inventory/v1 is
# not uploaded. The drift summary has a run_id of its own, made from
# drift.json.
#
# Usage: control-plane.sh check|upload
#
# check validates the inputs, before anything long has run. upload exports
# and sends; it writes control-plane-run-id (the run_id of the run document)
# to $GITHUB_OUTPUT once every document was accepted (a 2xx).
#
# The URL must be https, or http for localhost. The tokens are masked and
# never printed, and go to curl in a file, not in its arguments. A 5xx, a
# 429 and a connection failure are retried with a growing delay; any other
# answer fails at once, with the reason the control plane gives in its JSON
# body, {"error": "..."}. A failure fails the step, unless OPTIONAL is true.
#
# Inputs, as variables: URL, AUDIENCE, DROP_TAGS, DROP_TAG, DROP_ADDRESSES,
# HASH_IDS, HASH_KEY (else INFRAHARVEST_EXPORT_KEY), REQUEST_ID, OPTIONAL;
# and, for upload, BINARY, OUT, IMPORT, IMPORT_OUTCOME (the Import step's
# outcome; skipped when it didn't run), DRIFT (off, report or accept),
# DRIFT_OUTCOME (the Drift step's), DRIFT_STARTED_AT and DRIFT_FINISHED_AT
# (its start and finish), STARTED_AT and FINISHED_AT (when the
# import started and finished, RFC 3339, from the Import step; either may be
# empty), and the runner's
# ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN.
# INFRAHARVEST_UPLOAD_RETRY_DELAY is the first delay in seconds between
# attempts (default 2, doubling), for tests.
set -euo pipefail

# shellcheck source=action/output.sh
source "$(dirname "${BASH_SOURCE[0]}")/output.sh"

attempts=4

url="${URL-}"
audience="${AUDIENCE:-infraharvest-api}"
optional="${OPTIONAL-false}"
hash_ids="${HASH_IDS-false}"
drop_tags="${DROP_TAGS-false}"
drop_addresses="${DROP_ADDRESSES-false}"
request_id="${REQUEST_ID-}"

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
  for input in "control-plane-optional=$optional" "control-plane-hash-ids=$hash_ids" "control-plane-drop-tags=$drop_tags" "control-plane-drop-addresses=$drop_addresses"; do
    case "${input#*=}" in
      true | false) ;;
      *) fail "${input%%=*} must be true or false, got '${input#*=}'" ;;
    esac
  done
  # The audience goes into the query of the token request as it is.
  if [[ ! "$audience" =~ ^[A-Za-z0-9._~:/-]+$ ]]; then
    fail "control-plane-audience may hold letters, digits and . _ ~ : / - only, got '$audience'"
  fi
  # The ID goes into the run document, and the control plane matches it.
  if [[ -n "$request_id" && ! "$request_id" =~ ^[A-Za-z0-9._:-]{1,128}$ ]]; then
    fail "control-plane-request-id must be 1 to 128 letters, digits or . _ : -, got '$request_id'"
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

# read_run_id <document>: sets doc_run_id to the run_id of the exported
# document, checked as a header can hold it.
read_run_id() {
  doc_run_id=$(jq -r '.run_id // empty' "$tmp/$1.json")
  [[ -n "$doc_run_id" ]] || problem "the $1 document has no run_id"
  # The run_id goes into a header.
  [[ "$doc_run_id" =~ ^[A-Za-z0-9._:-]+$ ]] || problem "the $1 document's run_id has characters that can't go in a header"
}

upload() {
  [[ -n "$url" ]] || return 0
  local bin out report drift_report import_ran=false drift_ran=false send_import=false send_drift=false
  set_base
  bin="${BINARY:-infraharvest}"
  out="${OUT:-infrastructure}"
  report="$out/_reports/report.json"
  drift_report="$out/_reports/drift.json"

  # What ran: the Import step, unless it was skipped, such as after a drift
  # check that failed, and the Drift step, when it ran to its end.
  if [[ "${IMPORT-}" == true && "${IMPORT_OUTCOME-}" != skipped ]]; then import_ran=true; fi
  case "${DRIFT-off}" in
    report | accept)
      case "${DRIFT_OUTCOME-}" in
        success | failure) drift_ran=true ;;
      esac
      ;;
  esac

  # The import can have failed before it wrote a report, and the drift check
  # before it wrote drift.json: nothing of that to send, and the step's own
  # failure is the one to show.
  if [[ "$import_ran" == true ]]; then
    if unlinked "$report"; then
      send_import=true
    elif [[ "${IMPORT_OUTCOME-}" == failure ]]; then
      annotation warning "control plane: the import left no report to send ($report)"
    else
      problem "no report to send: $report isn't a regular file that no symbolic link leads to"
    fi
  fi
  if [[ "$drift_ran" == true ]]; then
    if unlinked "$drift_report"; then
      send_drift=true
    else
      annotation warning "control plane: the drift check left no drift report to send ($drift_report)"
    fi
  fi
  if [[ "$send_import" != true && "$send_drift" != true ]]; then return 0; fi

  tmp=$(umask 077; mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/infraharvest-control-plane.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT

  command -v jq > /dev/null || problem "jq is needed to read the answers and the run's ID and isn't installed"
  command -v gzip > /dev/null || problem "gzip is needed to compress the documents and isn't installed"

  # The session comes first, so that a refusal (a token the control plane
  # doesn't take, a plan that doesn't allow the upload) shows before the
  # export has run.
  open_session

  # The redaction flags apply to the OCSF inventory, which has the tags, IDs
  # and names, and --hash-ids to the graph too, which has the IDs, and to the
  # drift summary, which has the addresses. --drop-addresses applies to the
  # OCSF inventory and the drift summary.
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
  # run (started_at, finished_at), and the finish to the OCSF inventory (the
  # time of its events), so that sending the same run again changes neither.
  # A drift check's own start and finish go to its run document.
  local times=() finish=()
  [[ -n "${STARTED_AT-}" ]] && times+=("--started-at=$STARTED_AT")
  [[ -n "${FINISHED_AT-}" ]] && times+=("--finished-at=$FINISHED_AT") && finish=("--finished-at=$FINISHED_AT")
  local drift_times=()
  [[ -n "${DRIFT_STARTED_AT-}" ]] && drift_times+=("--started-at=$DRIFT_STARTED_AT")
  [[ -n "${DRIFT_FINISHED_AT-}" ]] && drift_times+=("--finished-at=$DRIFT_FINISHED_AT")

  # With an import, the run document is the import's (export's default when
  # both reports are there); without one, it is the drift check's, which
  # export takes from drift.json, not an old report.json the repository has.
  local docs=() doc
  if [[ "$send_import" == true ]]; then docs=(ocsf-inventory run gate graph); else docs=(run); fi
  if [[ "$send_drift" == true ]]; then docs+=(drift-summary); fi

  for doc in "${docs[@]}"; do
    flags=()
    case "$doc" in
      ocsf-inventory)
        # The repository and the commit come from the GITHUB_* variables of
        # the job, which export reads.
        flags=(${redact[@]+"${redact[@]}"} ${finish[@]+"${finish[@]}"})
        if [[ "$drop_addresses" == true ]]; then flags+=(--drop-addresses); fi
        ;;
      run)
        if [[ "$send_import" == true ]]; then flags=(${times[@]+"${times[@]}"}); else flags=(--command=drift ${drift_times[@]+"${drift_times[@]}"}); fi
        if [[ -n "$request_id" ]]; then flags+=("--request-id=$request_id"); fi
        ;;
      graph | drift-summary)
        if [[ "$hash_ids" == true ]]; then flags=(--hash-ids); fi
        if [[ "$doc" == drift-summary && "$drop_addresses" == true ]]; then flags+=(--drop-addresses); fi
        ;;
    esac
    if ! err=$(INFRAHARVEST_EXPORT_KEY="$key" "$bin" export "$out" "--schema=$doc" "--out-file=$tmp/$doc.json" ${flags[@]+"${flags[@]}"} 2>&1 > /dev/null); then
      problem "infraharvest export ($doc) failed: $err"
    fi
    if ! gzip -n -c "$tmp/$doc.json" > "$tmp/$doc.json.gz"; then
      problem "couldn't compress the $doc document"
    fi
  done

  read_run_id run
  run_id=$doc_run_id

  for doc in "${docs[@]}"; do
    # The drift summary's run is the drift check's, which an import's isn't.
    read_run_id "$doc"
    if ! http_request "$base/api/v1/ingest/$doc" "$tmp/headers" "$tmp/response" \
      -X POST -H 'Content-Type: application/json' -H 'Content-Encoding: gzip' \
      -H "Idempotency-Key: $doc_run_id:$doc" --data-binary "@$tmp/$doc.json.gz"; then
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
doc_run_id=
http_code=
case "${1-}" in
  check) check ;;
  upload) upload ;;
  *)
    echo "usage: control-plane.sh check|upload" >&2
    exit 2
    ;;
esac
