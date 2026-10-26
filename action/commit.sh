#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Commits the output to a branch and pushes it, without a pull request (the
# Commit step of action.yml, with commit set). It commits the paths a pull
# request would add, one a line in ADD_PATHS (the out directory and, with
# drift, the decisions file), as git adds them: a .gitignore, such as the
# one generated in out, keeps what it ignores out. The commit goes on top
# of the commit checked out, and is pushed to BRANCH, or to the branch
# checked out without one; the push never forces, so a branch that has
# moved on since the checkout fails it. It writes the commit's SHA to
# $GITHUB_OUTPUT as commit-sha, empty when nothing changed.
#
# Usage: TOKEN=<token> ADD_PATHS=<paths> COMMIT_MESSAGE=<message>
#   [BRANCH=<branch>] commit.sh
set -euo pipefail

fail() {
  echo "::error::$1"
  exit 1
}

branch="${BRANCH-}"
if [[ -z "$branch" ]]; then
  # A pull request's checkout, and a checkout of a tag or a SHA, have no
  # branch to push to.
  branch=$(git symbolic-ref --quiet --short HEAD) ||
    fail "commit: the checkout has no branch (a detached HEAD, as a pull request's or a tag's checkout has); set commit-branch to the branch to push to"
fi
git check-ref-format --branch "$branch" > /dev/null 2>&1 || fail "commit-branch: '$branch' isn't a valid branch name"

paths=()
while IFS= read -r path; do
  [[ -z "$path" ]] && continue
  # A path that is gone but was committed is a deletion to commit.
  if [[ -e "$path" ]] || git ls-files --error-unmatch -- "$path" > /dev/null 2>&1; then
    paths+=("$path")
  fi
done <<< "$ADD_PATHS"

if ((${#paths[@]} > 0)); then git add --all -- "${paths[@]}"; fi
if ((${#paths[@]} == 0)) || git diff --cached --quiet -- "${paths[@]}"; then
  echo "::notice::commit: the output hasn't changed; nothing to commit or push"
  echo "commit-sha=" >> "$GITHUB_OUTPUT"
  exit 0
fi

# The workflow's own identity, unless the job has set one.
if [[ -z "$(git config user.name || true)" ]]; then export GIT_AUTHOR_NAME="github-actions[bot]" GIT_COMMITTER_NAME="github-actions[bot]"; fi
if [[ -z "$(git config user.email || true)" ]]; then
  export GIT_AUTHOR_EMAIL="41898282+github-actions[bot]@users.noreply.github.com"
  export GIT_COMMITTER_EMAIL="41898282+github-actions[bot]@users.noreply.github.com"
fi
# Only the output: anything else the job staged stays out of the commit.
git commit --quiet --message "$COMMIT_MESSAGE" -- "${paths[@]}"
sha=$(git rev-parse HEAD)

# The token goes in a header of this push only. An empty extraheader first
# drops the one actions/checkout keeps, unless persist-credentials is
# false, so that the push is the token's.
server="${GITHUB_SERVER_URL:-https://github.com}"
auth=$(printf 'x-access-token:%s' "$TOKEN" | base64 | tr -d '\n')
echo "::add-mask::$auth"
log="$RUNNER_TEMP/infraharvest-push.log"
if ! git -c "http.$server/.extraheader=" -c "http.$server/.extraheader=AUTHORIZATION: basic $auth" \
  push --porcelain "$server/$GITHUB_REPOSITORY.git" "HEAD:refs/heads/$branch" > "$log" 2>&1; then
  cat "$log"
  if grep -q -e 'non-fast-forward' -e 'fetch first' -e '\[rejected\]' "$log"; then
    fail "commit: $branch has commits this checkout doesn't have, so the push was rejected (non-fast-forward); check out the head of $branch, or set commit-branch to another branch"
  fi
  if grep -q -e 'protected branch' -e 'GH006' -e 'GH013' "$log"; then
    fail "commit: a branch protection rule or ruleset rejected the push to $branch; push to a branch it doesn't protect, or use pull-request"
  fi
  fail "commit: couldn't push to $branch; see the log above"
fi
echo "::notice::commit: pushed $sha to $branch"
echo "commit-sha=$sha" >> "$GITHUB_OUTPUT"
