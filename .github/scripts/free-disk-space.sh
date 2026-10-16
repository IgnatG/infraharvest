#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Frees disk space on a GitHub-hosted Ubuntu runner, whose free space is too
# little for this module's dependencies or for the AWS provider (~700 MB
# unpacked) installed twice. Deleting the preinstalled SDKs takes over a
# minute, so `start` deletes them in the background, the most space per
# second first, and `wait`, just before the step that needs the space,
# returns once $FREE_DISK_GB GB (default 25) are free or everything is
# deleted.
#
# Usage: free-disk-space.sh start | wait
set -euo pipefail

log=${RUNNER_TEMP:?}/free-disk-space.log
done_file=$RUNNER_TEMP/free-disk-space.done
want_gb=${FREE_DISK_GB:-25}

available_gb() {
  df --output=avail -BG / | tail -n 1 | tr -dc 0-9
}

case ${1-} in
start)
  echo "free: $(available_gb) GB"
  # The runner leaves background processes running until the job ends.
  (
    for dir in /opt/hostedtoolcache/CodeQL /usr/share/dotnet /usr/local/lib/android /opt/ghc; do
      started=$SECONDS
      sudo rm -rf "$dir" || echo "couldn't delete $dir"
      echo "deleted $dir in $((SECONDS - started))s; free: $(available_gb) GB"
    done
    touch "$done_file"
  ) > "$log" 2>&1 < /dev/null &
  ;;
wait)
  started=$SECONDS
  until [ -e "$done_file" ] || [ "$(available_gb)" -ge "$want_gb" ]; do
    if [ $((SECONDS - started)) -ge 300 ]; then
      echo "::warning::gave up waiting for $want_gb GB of free disk space"
      break
    fi
    sleep 2
  done
  echo "waited $((SECONDS - started))s; free: $(available_gb) GB"
  cat "$log"
  ;;
*)
  echo "usage: $0 start | wait" >&2
  exit 2
  ;;
esac
