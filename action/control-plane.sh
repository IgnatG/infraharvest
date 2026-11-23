#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Sends the metadata of a run to a control plane (the Control plane step of
# action.yml, with control-plane-url set). It first trades the job's GitHub
# Actions OIDC token, for the audience AUDIENCE (default infraharvest-api),
# for a run token: POST <url>/api/v1/session with an empty body and the OIDC
# token in the header X-GitHub-OIDC-Token, answered with JSON,
# {"token": "...", "expires_at": "...", "limits": {...}}. Then it runs
# infraharvest export for the documents of the run, and POSTs each to
# <url>/api/v1/ingest/<document> as application/json, gzip-compressed
# (Content-Encoding: gzip), with the run
# token as a bearer token and the header Idempotency-Key: <run_id>:<document>,
# with :<GITHUB_RUN_ID> after it in GitHub Actions, so that two workflow runs
# with the same results are two runs to the control plane, and a re-run of one
# is the same.
# The OIDC token goes to the session endpoint only. Nothing else is sent. See
# docs/export.md. The log has one line for each document with its size in
# bytes, as exported and as gzipped, and nothing of its content.
#
# The documents are those of what ran: an import sends ocsf-inventory, run,
# gate and graph; a drift check sends drift-summary, and, when no import ran,
# the run document of the drift check (export --command drift) before it; both
# send the five. A discover run sends ocsf-inventory and run (export --command
# discover), made from the inventories it saved, and a verify run sends run
# and gate (export --command verify), made from verify.json. The inventory is
# sent as the OCSF inventory: inventory/v1 is not uploaded. The drift summary
# has a run_id of its own, made from drift.json.
#
# Usage: control-plane.sh check|upload
#
# check validates the inputs, before anything long has run. upload exports
# and sends; it writes control-plane-run-id (the run_id of the run document)
# to $GITHUB_OUTPUT once every document was accepted (a 2xx).
#
# The URL must be https, or http for localhost. The tokens are masked and
# never printed, and go to curl in a file, not in its arguments. A 5xx, a
# 429 and a connection failure that another try can pass (a refused or reset
# connection, a name that didn't resolve, a timeout; not a TLS or protocol
# error) are retried with a growing delay, which a 429's retryAfterMs (or
# Retry-After) replaces; any other answer fails at once, with the reason the
# control plane gives in its JSON body, {"error": "..."}. A request may take
# 60 seconds, and an upload 30 more for each whole megabyte of its body, 300 at
# most. A document over what the control plane takes fails the step before any
# is sent: 2 MB gzipped and 16 MB plain, unless the session answer's limits,
# {"max_body_bytes": n, "max_decompressed_bytes": n} (positive integers, 1000 to
# 100000000), say otherwise; limits that aren't valid are ignored. A failure
# fails the step, unless OPTIONAL is true.
#
# Inputs, as variables: URL, AUDIENCE, DROP_TAGS, DROP_TAG, DROP_ADDRESSES,
# HASH_IDS, HASH_KEY (else INFRAHARVEST_EXPORT_KEY), REQUEST_ID, OPTIONAL;
# and, for upload, BINARY, OUT, IMPORT, IMPORT_OUTCOME (the Import step's
# outcome; skipped when it didn't run), COMMAND (incremental when a control
# plane started an incremental import, which its run document then names;
# anything else is ignored), DISCOVER and VERIFY (true when the run is a discover or a
# verify run) with DISCOVER_OUTCOME and
# VERIFY_OUTCOME (their steps' outcomes), DRIFT (off, report or accept),
# DRIFT_OUTCOME (the Drift step's), DRIFT_STARTED_AT and DRIFT_FINISHED_AT
# (its start and finish), STARTED_AT and FINISHED_AT (when the
# import, discover or verify started and finished, RFC 3339, from its step;
# either may be empty), and the runner's
# ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN.
# INFRAHARVEST_UPLOAD_RETRY_DELAY is the first delay in seconds between
# attempts (default 2, doubling), and INFRAHARVEST_UPLOAD_MAX_WAIT the most
# seconds to wait when a 429 says when to try again (default 60), for tests.
set -euo pipefail

# shellcheck source=action/output.sh
source "$(dirname "${BASH_SOURCE[0]}")/output.sh"

attempts=4
# The seconds a request may take (curl's --max-time): this much for anything
# small, and for an upload this much plus an allowance for each megabyte of
# the body (see upload_max_time).
base_max_time=60
max_time=$base_max_time
# What the control plane takes, as sent (gzipped) and once decompressed, when its
# session answer says nothing (see read_limits).
max_gzip_bytes=2000000
max_plain_bytes=16000000

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
  # export would refuse a short key after the import has run.
  if [[ "$hash_ids" == true ]] && short_hash_key "${HASH_KEY:-${INFRAHARVEST_EXPORT_KEY-}}"; then
    fail "the hash key for control-plane-hash-ids must have at least 16 bytes: use a random one, such as the output of openssl rand -hex 32"
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

# transient_curl_exit <exit code>: whether a curl that got no answer failed in
# a way another try can get past: a name that didn't resolve (6), a refused
# connection (7), a timeout (28), an empty reply (52) or a send or receive
# that broke, such as a reset (55, 56). A TLS, protocol or usage error (35, 60,
# 1, 2, 3, 5 and the rest) comes out the same every time.
transient_curl_exit() {
  case "$1" in
    6 | 7 | 28 | 52 | 55 | 56) return 0 ;;
  esac
  return 1
}

# retry_after <body-file> <headers-file>: sets retry_ms to how long the
# control plane asked to wait, in milliseconds: the retryAfterMs of its JSON
# body, else a Retry-After header in seconds. Empty if it said nothing.
retry_after() {
  local secs
  retry_ms=
  if [[ -s "$1" ]]; then
    retry_ms=$(jq -r 'if type == "object" and (.retryAfterMs | type) == "number" and .retryAfterMs >= 0 then ([.retryAfterMs, 86400000] | min | floor) else empty end' "$1" 2> /dev/null) || retry_ms=
    # A body of several JSON values prints several lines: only one number counts.
    [[ "$retry_ms" =~ ^[0-9]{1,8}$ ]] || retry_ms=
  fi
  if [[ -z "$retry_ms" && -s "$2" ]]; then
    secs=$(grep -i '^retry-after:' "$2" | tail -n 1 | tr -d '\r[:space:]') || secs=
    secs=${secs#*:}
    if [[ "$secs" =~ ^[0-9]{1,6}$ ]]; then retry_ms=$((10#$secs * 1000)); fi
  fi
}

# seconds <milliseconds>: the time as seconds, for sleep and the log.
seconds() {
  printf '%d.%03d' $(($1 / 1000)) $(($1 % 1000))
}

# http_request <url> <header-file> <out-file> [curl-argument...]: a GET, or
# whatever the extra curl arguments make of it (-X POST and a body), with
# the headers of the file. A 5xx, a 429 and a connection failure that another
# try can get past (see transient_curl_exit) are retried; a 429 waits as long
# as the control plane asks, up to max_wait seconds, else the growing delay.
# The request takes at most max_time seconds. Sets http_code, and leaves the
# answer's body in out-file. Returns 0 on a 2xx.
http_request() {
  local target=$1 headers=$2 body=$3
  shift 3
  local args=(--silent --show-error --fail-with-body --connect-timeout 10 --max-time "$max_time"
    --max-redirs 0 -H "@$headers" -o "$body" -D "$tmp/response-headers" -w '%{http_code}' "$@")
  if [[ "$target" == https://* ]]; then args+=(--proto '=https'); else args+=(--proto '=http'); fi
  local attempt=1 delay=${INFRAHARVEST_UPLOAD_RETRY_DELAY-2} rc retry=false wait_ms
  local max_wait_ms=$((${INFRAHARVEST_UPLOAD_MAX_WAIT-60} * 1000))
  while :; do
    rc=0
    rm -f "$tmp/response-headers"
    http_code=$(curl "${args[@]}" "$target" 2> "$tmp/curl-error") || rc=$?
    http_code=${http_code:-000}
    if [[ "$http_code" =~ ^2[0-9][0-9]$ ]]; then return 0; fi
    # 000: curl got no answer. Only what can pass is tried again, such as a
    # refused connection or a timeout, not a TLS or protocol refusal. Any
    # other 4xx is a refusal, which another try wouldn't change.
    retry=false
    if [[ "$http_code" == 000 ]]; then
      if transient_curl_exit "$rc"; then retry=true; fi
    elif [[ "$http_code" == 429 || "$http_code" =~ ^5[0-9][0-9]$ ]]; then
      retry=true
    fi
    if [[ "$retry" == true ]] && ((attempt < attempts)); then
      wait_ms=$((delay * 1000))
      if [[ "$http_code" == 429 ]]; then
        retry_after "$body" "$tmp/response-headers"
        if [[ -n "$retry_ms" ]]; then
          wait_ms=$((retry_ms < max_wait_ms ? retry_ms : max_wait_ms))
        fi
      fi
      annotation notice "control plane: attempt $attempt got $http_code (curl exit $rc); trying again in $(seconds "$wait_ms")s"
      sleep "$(seconds "$wait_ms")"
      delay=$((delay * 2))
      attempt=$((attempt + 1))
      continue
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

# read_limits <session-response>: sets max_gzip_bytes and max_plain_bytes to the
# limits the session answer reports, {"limits": {"max_body_bytes": n,
# "max_decompressed_bytes": n}}, when both are there, each a whole number from
# 1000 to 100000000. An answer without limits, such as from a control plane that
# doesn't report them, keeps the defaults; one whose limits aren't valid keeps
# them too, and says so.
read_limits() {
  local found limits gzip plain
  found=$(jq -r 'if type == "object" and has("limits") then "yes" else empty end' "$1" 2> /dev/null) || found=
  [[ -n "$found" ]] || return 0
  limits=$(jq -r '
    def whole: type == "number" and . == floor and . >= 1000 and . <= 100000000;
    if (.limits | type) == "object" and (.limits.max_body_bytes | whole) and (.limits.max_decompressed_bytes | whole)
    then "\(.limits.max_body_bytes) \(.limits.max_decompressed_bytes)" else empty end' "$1" 2> /dev/null) || limits=
  if [[ ! "$limits" =~ ^([0-9]+)\ ([0-9]+)$ ]]; then
    annotation warning "control plane: the session answer's limits aren't two whole numbers of bytes from 1000 to 100000000: using the defaults, $max_gzip_bytes gzipped and $max_plain_bytes plain"
    return 0
  fi
  gzip=${BASH_REMATCH[1]}
  plain=${BASH_REMATCH[2]}
  if ((gzip != max_gzip_bytes || plain != max_plain_bytes)); then
    annotation notice "control plane: it takes a document of $gzip bytes gzipped and $plain bytes plain, as its session answer says"
  fi
  max_gzip_bytes=$gzip
  max_plain_bytes=$plain
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
  read_limits "$tmp/session-response"
  rm -f "$tmp/session-response" "$tmp/session-headers"
  if [[ ! "$run_token" =~ ^[[:graph:]]+$ ]]; then
    problem "the session answer at $base/api/v1/session has no usable token"
  fi
  printf '::add-mask::%s\n' "$run_token"
  ( umask 077; printf 'Authorization: Bearer %s\n' "$run_token" > "$tmp/headers" )
  annotation notice "control plane: opened a session${expires:+ (the token expires at $expires)}"
}

# log_size <document>: one log line with the size in bytes of the exported
# document and of the gzip-compressed body that is sent, so that runs show how
# large the documents get. Only the name of the document and two numbers: none
# of its content.
log_size() {
  local json gz
  json=$(wc -c < "$tmp/$1.json")
  gz=$(wc -c < "$tmp/$1.json.gz")
  echo "control plane: the $1 document is ${json//[[:space:]]/} bytes, ${gz//[[:space:]]/} bytes gzipped"
}

# upload_max_time <file>: sets max_time for sending file: base_max_time, plus
# 30 seconds for each whole megabyte of it, at most 300. A large
# estate's inventory sent over a slow runner's link takes longer than a fixed
# minute, and a timeout is retried three more times, then fails the run.
upload_max_time() {
  local bytes mb
  bytes=$(wc -c < "$1")
  bytes=${bytes//[[:space:]]/}
  mb=$((bytes / 1048576))
  max_time=$((base_max_time + 30 * mb))
  if ((max_time > 300)); then max_time=300; fi
}

# check_size <document>: the control plane takes a body of max_gzip_bytes at most
# as sent (gzipped) and max_plain_bytes once decompressed: 2 MB and 16 MB, as
# the ingest routes of the control plane the Action is made for read it
# (MAX_BODY_BYTES in src/features/api/body.ts and MAX_DECOMPRESSED_BYTES in
# src/features/api/ingest-body.ts), unless its session answer says otherwise
# (read_limits). A document over either
# fails here, before any is sent, so that a run is never left with some of its
# documents stored and the rest refused for their size; one within 10% of
# either says so.
check_size() {
  local json gz
  json=$(wc -c < "$tmp/$1.json")
  gz=$(wc -c < "$tmp/$1.json.gz")
  json=${json//[[:space:]]/}
  gz=${gz//[[:space:]]/}
  if ((gz > max_gzip_bytes || json > max_plain_bytes)); then
    problem "the $1 document is too large for the control plane: ${json} bytes, ${gz} bytes gzipped, against at most ${max_plain_bytes} and ${max_gzip_bytes}; nothing was sent. Send less of the estate, such as with control-plane-drop-tags, or import fewer roots in this run"
  fi
  if ((gz * 10 >= max_gzip_bytes * 9 || json * 10 >= max_plain_bytes * 9)); then
    annotation warning "control plane: the $1 document is ${json} bytes, ${gz} bytes gzipped, within 10% of what the control plane takes (${max_plain_bytes} and ${max_gzip_bytes}); a larger estate won't upload"
  fi
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
  local bin out report drift_report inventories verify_report import_ran=false drift_ran=false send_import=false send_drift=false send_discover=false send_verify=false
  set_base
  bin="${BINARY:-infraharvest}"
  out="${OUT:-infrastructure}"
  report="$out/_reports/report.json"
  drift_report="$out/_reports/drift.json"
  inventories="$out/.infraharvest/inventory"
  verify_report="$out/_reports/verify.json"

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
  # failure is the one to show. The steps remove the file they write before
  # they run (discard in output.sh), so what is there is what this run wrote,
  # never one an earlier run committed.
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
  # A discover run left the inventories it saved, and a verify run its
  # verify.json, unless it failed before: then the step's own failure is the
  # one to show, as for an import. After a success they must be there.
  if [[ "${DISCOVER-}" == true && "${DISCOVER_OUTCOME-}" != skipped ]]; then
    if ! linked "$inventories" && compgen -G "$inventories/*.json" > /dev/null; then
      send_discover=true
    elif [[ "${DISCOVER_OUTCOME-}" == failure ]]; then
      annotation warning "control plane: discover left no inventory to send ($inventories)"
    else
      problem "no inventory to send: $inventories has no file that no symbolic link leads to"
    fi
  fi
  if [[ "${VERIFY-}" == true && "${VERIFY_OUTCOME-}" != skipped ]]; then
    if unlinked "$verify_report"; then
      send_verify=true
    elif [[ "${VERIFY_OUTCOME-}" == failure ]]; then
      annotation warning "control plane: verify left no report to send ($verify_report)"
    else
      problem "no report to send: $verify_report isn't a regular file that no symbolic link leads to"
    fi
  fi
  if [[ "$send_import" != true && "$send_drift" != true && "$send_discover" != true && "$send_verify" != true ]]; then return 0; fi

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
  # OCSF inventory, the graph (which has the addresses of its nodes) and the
  # drift summary.
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
  # time of its events) and the graph, so that sending the same run again
  # changes none of them.
  # The start and finish of a discover or verify run go to its run document
  # the same way. A drift check's own start and finish go to its run
  # document, and its finish to the drift summary.
  local times=() finish=()
  [[ -n "${STARTED_AT-}" ]] && times+=("--started-at=$STARTED_AT")
  [[ -n "${FINISHED_AT-}" ]] && times+=("--finished-at=$FINISHED_AT") && finish=("--finished-at=$FINISHED_AT")
  local drift_times=() drift_finish=()
  [[ -n "${DRIFT_STARTED_AT-}" ]] && drift_times+=("--started-at=$DRIFT_STARTED_AT")
  [[ -n "${DRIFT_FINISHED_AT-}" ]] && drift_times+=("--finished-at=$DRIFT_FINISHED_AT") && drift_finish=("--finished-at=$DRIFT_FINISHED_AT")

  # With an import, the run document is the import's (export's default when
  # both reports are there); without one, it is the drift check's, which
  # export takes from drift.json, not an old report.json the repository has.
  local docs=() doc
  if [[ "$send_import" == true ]]; then
    docs=(ocsf-inventory run gate graph)
  elif [[ "$send_discover" == true ]]; then
    docs=(ocsf-inventory run)
  elif [[ "$send_verify" == true ]]; then
    docs=(run gate)
  else
    docs=(run)
  fi
  if [[ "$send_drift" == true ]]; then docs+=(drift-summary); fi

  for doc in "${docs[@]}"; do
    flags=()
    case "$doc" in
      ocsf-inventory)
        # The repository and the commit come from the GITHUB_* variables of
        # the job, which export reads.
        flags=(${redact[@]+"${redact[@]}"} ${finish[@]+"${finish[@]}"})
        # The configuration file says where each root's state is.
        if [[ -n "${CONFIG-}" ]]; then flags+=("--config=$CONFIG"); fi
        if [[ "$drop_addresses" == true ]]; then flags+=(--drop-addresses); fi
        if [[ "$send_discover" == true ]]; then flags+=(--command=discover); fi
        ;;
      run)
        if [[ "$send_import" == true ]]; then
          # An incremental import a control plane asked for says so: it has the
          # import's report, so the run document keeps the import's fields.
          if [[ "${COMMAND-}" == incremental ]]; then flags=(--command=incremental); fi
          flags+=(${times[@]+"${times[@]}"})
        elif [[ "$send_discover" == true ]]; then
          flags=(--command=discover ${times[@]+"${times[@]}"})
        elif [[ "$send_verify" == true ]]; then
          flags=(--command=verify ${times[@]+"${times[@]}"})
        else
          flags=(--command=drift ${drift_times[@]+"${drift_times[@]}"})
        fi
        if [[ -n "$request_id" ]]; then flags+=("--request-id=$request_id"); fi
        ;;
      gate)
        if [[ "$send_verify" == true ]]; then flags=(--command=verify); fi
        ;;
      graph | drift-summary)
        # The graph's generated_at is the import's finish and the drift
        # summary's the drift check's, as the inventory's is, so that
        # exporting the same run again gives the same document.
        if [[ "$doc" == graph ]]; then
          flags=(${finish[@]+"${finish[@]}"})
        else
          flags=(${drift_finish[@]+"${drift_finish[@]}"})
        fi
        if [[ "$hash_ids" == true ]]; then flags+=(--hash-ids); fi
        if [[ "$drop_addresses" == true ]]; then flags+=(--drop-addresses); fi
        ;;
    esac
    if ! err=$(INFRAHARVEST_EXPORT_KEY="$key" "$bin" export "$out" "--schema=$doc" "--out-file=$tmp/$doc.json" ${flags[@]+"${flags[@]}"} 2>&1 > /dev/null); then
      problem "infraharvest export ($doc) failed: $err"
    fi
    if ! gzip -n -c "$tmp/$doc.json" > "$tmp/$doc.json.gz"; then
      problem "couldn't compress the $doc document"
    fi
    log_size "$doc"
    check_size "$doc"
  done

  read_run_id run
  run_id=$doc_run_id

  for doc in "${docs[@]}"; do
    # The drift summary's run is the drift check's, which an import's isn't.
    read_run_id "$doc"
    upload_max_time "$tmp/$doc.json.gz"
    if ! http_request "$base/api/v1/ingest/$doc" "$tmp/headers" "$tmp/response" \
      -X POST -H 'Content-Type: application/json' -H 'Content-Encoding: gzip' \
      -H "Idempotency-Key: $doc_run_id:$doc${GITHUB_RUN_ID:+:$GITHUB_RUN_ID}" --data-binary "@$tmp/$doc.json.gz"; then
      problem "sending the $doc document to $base/api/v1/ingest/$doc failed: $(describe "$tmp/response" "upload")"
    fi
    max_time=$base_max_time
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
