#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Works out the pull request the action opens (the Pull request details
# step of action.yml): its title, commit message and branch, its body, the
# import report then the drift report, written to a file under
# $RUNNER_TEMP, and the paths it adds, the out directory and, with drift,
# the decisions file. It writes them to $GITHUB_OUTPUT as title,
# commit-message, branch, body-path and add-paths, for
# peter-evans/create-pull-request, and for commit.sh, which commits the
# same paths with the same message. It runs no git and needs no token, so
# action-test runs it on its own.
#
# Usage: IMPORT=true|false DRIFT=off|report|accept OUT=<dir>
#   [DRIFT_DECISIONS=<file>] [BRANCH=<branch>] [ENVIRONMENT=<env>]
#   [IMPORT_REPORT=<report.md>] [DRIFT_REPORT=<drift.md>]
#   pull-request-details.sh
set -euo pipefail

# shellcheck source=action/output.sh
source "$(dirname "${BASH_SOURCE[0]}")/output.sh"

# drift accept writes accepted changes into the code; drift report accepts
# nothing, and records the changes, in the decisions file and the report,
# for review.
case "$DRIFT/$IMPORT" in
  off/*)
    title="Import existing resources with infraharvest"
    commit="feat: import existing resources with infraharvest"
    branch=infraharvest/import
    ;;
  accept/true)
    title="Accept cloud changes (drift) and import new resources"
    commit="fix: accept drift found by infraharvest"
    branch=infraharvest/import
    ;;
  accept/*)
    title="Accept cloud changes (drift)"
    commit="fix: accept drift found by infraharvest"
    branch=infraharvest/drift
    ;;
  report/true)
    title="Import new resources, and record cloud changes (drift) for review"
    commit="feat: import new resources and record drift found by infraharvest"
    branch=infraharvest/import
    ;;
  *)
    title="Record cloud changes (drift) for review"
    commit="chore: record drift found by infraharvest for review"
    branch=infraharvest/drift
    ;;
esac
# Drift checked as some environments, in a job per environment: each
# job's pull request is that environment's, on a branch of its own, so
# that the jobs of a matrix don't push to one branch.
if [[ "$DRIFT" != off && -n "${ENVIRONMENT-}" && "$ENVIRONMENT" != all ]]; then
  title="$title in $ENVIRONMENT"
  commit="$commit in $ENVIRONMENT"
  branch="$branch-${ENVIRONMENT//,/-}"
fi
if [[ -n "${BRANCH-}" ]]; then branch="$BRANCH"; fi
# The body is the import's report, then the drift report, but not through
# a symbolic link the repository commits (see unlinked in output.sh).
body="$RUNNER_TEMP/infraharvest-pull-request.md"
: > "$body"
for file in "${IMPORT_REPORT-}" "${DRIFT_REPORT-}"; do
  [[ -z "$file" ]] && continue
  if unlinked "$file"; then
    if [[ -s "$body" ]]; then printf '\n' >> "$body"; fi
    cat "$file" >> "$body"
  elif [[ -e "$file" || -L "$file" ]]; then
    annotation warning "$file isn't a regular file that no symbolic link leads to: leaving it out of the pull request"
  fi
done
output title "$title"
output commit-message "$commit"
output branch "$branch"
output body-path "$body"
# The decisions file, wherever it is, is part of the change.
paths=("$OUT")
if [[ "$DRIFT" != off && -n "${DRIFT_DECISIONS-}" ]]; then paths+=("$DRIFT_DECISIONS"); fi
multiline_output add-paths "${paths[@]}"
