#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The scheduled clean-up's last word on a Google Cloud sandbox project
# (sandbox-leftovers.yml), after sandbox-google-sweep.sh: finds, without a
# list of kinds kept by hand, what is still the sandbox's, and fails when
# anything is.
#
#   - One Cloud Asset Inventory search of every kind of resource in the
#     project, kept to what is labelled infraharvest-sandbox or named
#     ih-sbx- (ih_sbx_ for BigQuery), which covers the kinds that take no
#     labels too.
#   - KMS key rings and keys can't be deleted, and every run leaves its
#     own: they don't count, but a key version of theirs still enabled or
#     disabled does, as the sweep schedules their destruction.
#
# Cloud Asset Inventory lists what was deleted minutes ago for a while, so
# it searches again, five minutes apart, up to half an hour, before what
# it still finds counts.
#
# What is left is printed as "<project> <type> <name>", one per line, and
# added to $LEFTOVERS_FILE when it is set: names and IDs only.
#
# Usage: CLOUDSDK_CORE_PROJECT=<project> [SANDBOX_GOOGLE_REGION=<region>] [LEFTOVERS_FILE=<file>] sandbox-google-leftovers.sh
# Signed in to gcloud as the fixture service account, whose Viewer role
# searches Cloud Asset Inventory.
set -euo pipefail

project=${CLOUDSDK_CORE_PROJECT:?}
region=${SANDBOX_GOOGLE_REGION:-europe-west2}

# search prints what Cloud Asset Inventory holds of the sandbox's, but its
# KMS key rings, keys and key versions, as "<type> <name>".
search() {
  gcloud asset search-all-resources --scope "projects/$project" --format json |
    jq -r '.[]
      | select((.name | test("/ih[-_]sbx[-_]"))
        or ((.displayName // "") | test("^ih[-_]sbx[-_]"))
        or ((.labels // {}) | has("infraharvest-sandbox")))
      | select(.assetType | startswith("cloudkms.googleapis.com/") | not)
      | "\(.assetType) \(.name | sub("^//[^/]+/projects/[^/]+/"; "")) \(.displayName // "")"'
}

# keys prints the key versions of the sandbox's key rings still enabled or
# disabled, whose destruction isn't scheduled.
keys() {
  local ring key
  for ring in $(gcloud kms keyrings list --location "$region" --filter "name ~ '/keyRings/ih-sbx-'" --format 'value(name)'); do
    for key in $(gcloud kms keys list --keyring "$ring" --format 'value(name)'); do
      gcloud kms keys versions list --key "$key" --filter 'state=ENABLED OR state=DISABLED' \
        --format 'value(name)' | sed 's|^projects/[^/]*/|cloudkms.googleapis.com/CryptoKeyVersion |'
    done
  done
}

for round in 1 2 3 4 5 6 7; do
  # One assignment each, so that a failed search stops the script.
  found=$(search)
  versions=$(keys)
  left=$(printf '%s\n%s\n' "$found" "$versions" | sed '/^$/d')
  if [ -z "$left" ] || [ "$round" -eq 7 ]; then
    break
  fi
  echo "Cloud Asset Inventory still lists $(wc -l <<< "$left") of the sandbox's: looking again in five minutes"
  sleep 300
done
left=$(sed "/^$/d; s|^|$project |" <<< "$left")
if [ -n "${LEFTOVERS_FILE-}" ] && [ -n "$left" ]; then
  printf '%s\n' "$left" >> "$LEFTOVERS_FILE"
fi
if [ -n "$left" ]; then
  echo "::error::Left in $project after the clean-up:"
  printf '%s\n' "$left"
  exit 1
fi
echo "Nothing left in $project."
