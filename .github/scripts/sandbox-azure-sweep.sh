#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The sandbox-azure workflow's sweep, which its round trip runs when it
# failed or found leftovers, and its clean-up job when the round trip was
# cancelled, timed out or failed: deletes what Terraform's state doesn't
# hold, such as a resource whose creation failed half-way, or what a run
# whose destroy never finished left. The locks first, which would stop the
# rest, the estate's role assignments and custom roles, then every
# resource tagged infraharvest-sandbox, whichever engine's run made it
# (runs never overlap), a few rounds over, as some can only go once what
# uses them has. The resource group itself stays. It fails if anything is
# left in the group.
#
# Usage: ARM_SUBSCRIPTION_ID=<id> SANDBOX_AZURE_RESOURCE_GROUP=<group> sandbox-azure-sweep.sh
# Signed in to the Azure CLI as the fixture identity.
set -euo pipefail

sub=(--subscription "${ARM_SUBSCRIPTION_ID:?}")
group=${SANDBOX_AZURE_RESOURCE_GROUP:?}
locks=$(az lock list "${sub[@]}" -g "$group" --query '[].id' -o tsv)
while IFS= read -r lock; do
  if [ -n "$lock" ]; then
    az lock delete "${sub[@]}" --ids "$lock" || true
  fi
done <<< "$locks"
# The estate's role assignments, by their description, and then
# its custom roles, by their name, both starting ih-sbx-: the
# bootstrap's, which the fixture's own roles come from, don't.
scope="/subscriptions/$ARM_SUBSCRIPTION_ID/resourceGroups/$group"
assignments=$(az role assignment list "${sub[@]}" --scope "$scope" --query "[?starts_with(description || '', 'ih-sbx-')].id" -o tsv)
while IFS= read -r assignment; do
  if [ -n "$assignment" ]; then
    az role assignment delete "${sub[@]}" --ids "$assignment" || true
  fi
done <<< "$assignments"
roles=$(az role definition list "${sub[@]}" --custom-role-only true --scope "$scope" --query "[?starts_with(roleName, 'ih-sbx-')].name" -o tsv)
while IFS= read -r role; do
  if [ -n "$role" ]; then
    az role definition delete "${sub[@]}" --name "$role" --scope "$scope" || true
  fi
done <<< "$roles"
query="[?tags.\"infraharvest-sandbox\" != null].id"
for _ in 1 2 3; do
  ids=$(az resource list "${sub[@]}" -g "$group" --query "$query" -o tsv)
  if [ -z "$ids" ]; then
    break
  fi
  while IFS= read -r id; do
    az resource delete "${sub[@]}" --ids "$id" || true
  done <<< "$ids"
done
left=$(az resource list "${sub[@]}" -g "$group" --query '[].id' -o tsv)
if [ -n "$left" ]; then
  echo "::error::Left in $group: $left"
  exit 1
fi
