#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Runs a command, and runs it again up to twice more, 15 and 30 seconds
# later, if it fails. For `init`, whose provider downloads from
# releases.hashicorp.com now and then fail with a reset connection; the
# engines retry the registry's API but not the downloads.
#
# Usage: retry.sh command [args...]
set -uo pipefail

delay=15
for attempt in 1 2 3; do
  "$@" && exit 0
  status=$?
  if [ "$attempt" -lt 3 ]; then
    echo "::warning::$1 failed (exit $status), trying again in ${delay}s" >&2
    sleep "$delay"
    delay=$((delay * 2))
  fi
done
exit "$status"
