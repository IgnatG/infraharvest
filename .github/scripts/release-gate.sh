#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Checks that the commit a release is made from passed the checks a
# release depends on, before the release workflow tags it. The release
# workflow's own job runs only `go test ./...`; the race, e2e and lint jobs
# and the sandbox round trips run elsewhere:
#
#   - Tests and MegaLinter run on pull requests, so they are looked for on
#     the release commit and, where the commit is the merge of a pull
#     request, on the pull request's last commit. A passing run on either
#     counts.
#   - The sandbox round trips are started by hand on main, so each workflow
#     in SANDBOXES must have passed on the release commit itself.
#
# For each workflow the latest run on a commit counts: a failed run that a
# later run passes is fine; a later failure is not. Runs still going count
# as not passed.
#
# Usage: release-gate.sh (with GH_TOKEN, GH_REPO and SHA set; gh and jq on
# PATH). PULL_REQUEST_CHECKS and SANDBOXES are newline-separated workflow
# names, defaulting to the ones below.
set -euo pipefail

repo=${GH_REPO:?}
sha=${SHA:?}
pull_request_checks=${PULL_REQUEST_CHECKS:-$'Tests\nMegaLinter'}
sandboxes=${SANDBOXES:-$'Sandbox (AWS)\nSandbox (Azure)\nSandbox (Google Cloud)'}

status=0
fail() {
  echo "::error::$*"
  status=1
}

# conclusions <sha>: "<workflow name><TAB><conclusion>" for the latest run
# of each workflow on the commit; "pending" for one still going.
conclusions() {
  gh api --paginate "repos/$repo/actions/runs?head_sha=$1&per_page=100" \
    --jq '.workflow_runs[] | [.name, (.conclusion // "pending"), .created_at] | @tsv' |
    sort -t $'\t' -k1,1 -k3,3 |
    awk -F '\t' '{ last[$1] = $2 } END { for (n in last) print n "\t" last[n] }'
}

# conclusion <table> <workflow>: the conclusion of the workflow in table, or
# "none".
conclusion() {
  awk -F '\t' -v name="$2" '$1 == name { print $2; found = 1 } END { if (!found) print "none" }' <<<"$1"
}

at_release=$(conclusions "$sha")
head=$(gh api "repos/$repo/commits/$sha/pulls" \
  --jq '[.[] | select(.merged_at != null)][0].head.sha // empty')
at_head=
if [ -n "$head" ]; then
  at_head=$(conclusions "$head")
  echo "$sha is the merge of a pull request whose last commit is $head"
fi

while IFS= read -r workflow; do
  [ -n "$workflow" ] || continue
  release=$(conclusion "$at_release" "$workflow")
  pr=none
  [ -z "$head" ] || pr=$(conclusion "$at_head" "$workflow")
  echo "$workflow: $release on the release commit, $pr on the pull request"
  if [ "$release" != success ] && [ "$pr" != success ]; then
    fail "$workflow hasn't passed on $sha${head:+ or on the last commit of its pull request, $head}"
  fi
done <<<"$pull_request_checks"

while IFS= read -r workflow; do
  [ -n "$workflow" ] || continue
  release=$(conclusion "$at_release" "$workflow")
  echo "$workflow: $release on the release commit"
  if [ "$release" != success ]; then
    fail "$workflow hasn't passed on $sha: run it on main (gh workflow run) before releasing"
  fi
done <<<"$sandboxes"

exit "$status"
