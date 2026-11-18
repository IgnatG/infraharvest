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
#   - A passing Tests run isn't enough: a pull request runs a cloud's e2e
#     jobs only when it changes what that cloud's build reads, and the
#     workflow counts a skipped job as passed. So for each cloud, the
#     latest Tests run on the release commit or on the pull request's last
#     commit must have run that cloud's e2e jobs, and all of them must have
#     succeeded. A pull request that left a cloud alone doesn't qualify: run
#     Tests on main by hand (gh workflow run test.yml --ref main), which
#     runs every job.
#   - The sandbox round trips are started by hand on main, so each workflow
#     in SANDBOXES must have passed on the release commit itself.
#
# For each workflow the latest run on a commit counts: a failed run that a
# later run passes is fine; a later failure is not. Runs still going count
# as not passed.
#
# Usage: release-gate.sh (with GH_TOKEN, GH_REPO and SHA set; gh and jq on
# PATH). PULL_REQUEST_CHECKS and SANDBOXES are newline-separated workflow
# names, and E2E_CLOUDS a newline-separated list of the clouds whose e2e
# jobs (named "e2e-<cloud> (...)" in Tests) must have run, defaulting to the
# ones below.
set -euo pipefail

repo=${GH_REPO:?}
sha=${SHA:?}
pull_request_checks=${PULL_REQUEST_CHECKS:-$'Tests\nMegaLinter'}
sandboxes=${SANDBOXES:-$'Sandbox (AWS)\nSandbox (Azure)\nSandbox (Google Cloud)'}
e2e_clouds=${E2E_CLOUDS:-$'aws\ngoogle\nazure'}

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

# e2e_jobs <sha>: "<job name><TAB><conclusion>" for the e2e jobs of the
# latest Tests run on the commit; nothing when it has no Tests run.
e2e_jobs() {
  local id
  id=$(gh api --paginate "repos/$repo/actions/runs?head_sha=$1&per_page=100" \
    --jq '.workflow_runs[] | select(.name == "Tests") | [.created_at, .id] | @tsv' |
    sort | tail -n 1 | cut -f2)
  [ -n "$id" ] || return 0
  gh api --paginate "repos/$repo/actions/runs/$id/jobs?per_page=100" \
    --jq '.jobs[] | select(.name | startswith("e2e-")) | [.name, (.conclusion // "pending")] | @tsv'
}

# e2e_state <table> <cloud>: "success" when the table has e2e jobs of the
# cloud and every one succeeded; otherwise what stopped it: "not run" for
# none, else the first conclusion that isn't success (a skipped job is
# "skipped").
e2e_state() {
  awk -F '\t' -v prefix="e2e-$2 (" '
    index($1, prefix) == 1 { n++; if ($2 != "success" && bad == "") bad = $2 }
    END { if (n == 0) print "not run"; else if (bad != "") print bad; else print "success" }' <<<"$1"
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

jobs_at_release=$(e2e_jobs "$sha")
jobs_at_head=
[ -z "$head" ] || jobs_at_head=$(e2e_jobs "$head")
while IFS= read -r cloud; do
  [ -n "$cloud" ] || continue
  release=$(e2e_state "$jobs_at_release" "$cloud")
  pr="not run"
  [ -z "$head" ] || pr=$(e2e_state "$jobs_at_head" "$cloud")
  echo "e2e-$cloud: $release on the release commit, $pr on the pull request"
  if [ "$release" != success ] && [ "$pr" != success ]; then
    fail "Tests hasn't run the e2e-$cloud jobs to success on $sha${head:+ or on the last commit of its pull request, $head}: run Tests on main (gh workflow run test.yml --ref main) before releasing"
  fi
done <<<"$e2e_clouds"

while IFS= read -r workflow; do
  [ -n "$workflow" ] || continue
  release=$(conclusion "$at_release" "$workflow")
  echo "$workflow: $release on the release commit"
  if [ "$release" != success ]; then
    fail "$workflow hasn't passed on $sha: run it on main (gh workflow run) before releasing"
  fi
done <<<"$sandboxes"

exit "$status"
