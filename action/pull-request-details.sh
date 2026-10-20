#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Works out the pull request the action opens (the Pull request details
# step of action.yml): its title, commit message and branch, its body, the
# import report then the drift report, written to a file under
# $RUNNER_TEMP, and the paths it adds, the out directory and, with drift,
# the decisions file. It writes them to $GITHUB_OUTPUT as title,
# commit-message, branch, body-path and add-paths, for
# peter-evans/create-pull-request. It runs no git and needs no token, so
# action-test runs it on its own.
#
# Usage: IMPORT=true|false DRIFT=off|report|accept OUT=<dir>
#   [DRIFT_DECISIONS=<file>] [BRANCH=<branch>] [IMPORT_REPORT=<report.md>]
#   [DRIFT_REPORT=<drift.md>] pull-request-details.sh
set -euo pipefail

if [[ "$DRIFT" == off ]]; then
  title="Import existing resources with infraharvest"
  commit="feat: import existing resources with infraharvest"
  branch=infraharvest/import
elif [[ "$IMPORT" == true ]]; then
  title="Accept cloud changes (drift) and import new resources"
  commit="fix: accept drift found by infraharvest"
  branch=infraharvest/import
else
  title="Accept cloud changes (drift)"
  commit="fix: accept drift found by infraharvest"
  branch=infraharvest/drift
fi
if [[ -n "${BRANCH-}" ]]; then branch="$BRANCH"; fi
# The body is the import's report, then the drift report.
body="$RUNNER_TEMP/infraharvest-pull-request.md"
: > "$body"
for file in "${IMPORT_REPORT-}" "${DRIFT_REPORT-}"; do
  if [[ -n "$file" && -f "$file" ]]; then
    if [[ -s "$body" ]]; then printf '\n' >> "$body"; fi
    cat "$file" >> "$body"
  fi
done
{
  echo "title=$title"
  echo "commit-message=$commit"
  echo "branch=$branch"
  echo "body-path=$body"
  # The decisions file, wherever it is, is part of the change.
  echo "add-paths<<INFRAHARVEST_PATHS"
  echo "$OUT"
  if [[ "$DRIFT" != off && -n "${DRIFT_DECISIONS-}" ]]; then echo "$DRIFT_DECISIONS"; fi
  echo "INFRAHARVEST_PATHS"
} >> "$GITHUB_OUTPUT"
