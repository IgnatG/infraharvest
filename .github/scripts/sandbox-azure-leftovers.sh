#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The scheduled clean-up's last word on an Azure sandbox subscription
# (sandbox-leftovers.yml), after sandbox-azure-sweep.sh: finds, without a
# list of kinds kept by hand, what is still the sandbox's, and fails when
# anything is.
#
#   - Every resource in the estate's resource group, which holds nothing
#     between runs.
#   - Every resource tagged infraharvest-sandbox elsewhere in the
#     subscription that the fixture identity can read. It can create
#     nothing outside its group, so this finds nothing unless the group
#     was changed, or someone else tagged something.
#   - The locks, role assignments and custom roles of the group named, or
#     described, ih-sbx-, which aren't resources of the group.
#
# Soft-deleted Key Vaults don't count: they cost nothing, go after seven
# days, and the fixture can't purge them.
#
# What is left is printed as "<resource group> <type> <name>", one per
# line, and added to $LEFTOVERS_FILE when it is set: names only, never a
# subscription ID, as the logs of a public repository's runs are public.
#
# Usage: ARM_SUBSCRIPTION_ID=<id> SANDBOX_AZURE_RESOURCE_GROUP=<group> [LEFTOVERS_FILE=<file>] sandbox-azure-leftovers.sh
# Signed in to the Azure CLI as the fixture identity.
set -euo pipefail

sub=(--subscription "${ARM_SUBSCRIPTION_ID:?}")
group=${SANDBOX_AZURE_RESOURCE_GROUP:?}
scope="/subscriptions/$ARM_SUBSCRIPTION_ID/resourceGroups/$group"
lower=$(tr '[:upper:]' '[:lower:]' <<< "$group")

# One assignment each, so that a failed listing stops the script.
resources=$(az resource list "${sub[@]}" -g "$group" --query '[].[resourceGroup, type, name]' -o tsv)
elsewhere=$(az resource list "${sub[@]}" --tag infraharvest-sandbox \
  --query "[?to_string(resourceGroup) != '$group' && to_string(resourceGroup) != '$lower'].[resourceGroup, type, name]" -o tsv)
locks=$(az lock list "${sub[@]}" -g "$group" --query "[?starts_with(name, 'ih-sbx-')].['$group', type, name]" -o tsv)
assignments=$(az role assignment list "${sub[@]}" --scope "$scope" \
  --query "[?starts_with(description || '', 'ih-sbx-')].['$group', type, name]" -o tsv)
roles=$(az role definition list "${sub[@]}" --custom-role-only true --scope "$scope" \
  --query "[?starts_with(roleName, 'ih-sbx-')].['$group', type, roleName]" -o tsv)
left=$(printf '%s\n' "$resources" "$elsewhere" "$locks" "$assignments" "$roles" | tr '\t' ' ' | sed '/^$/d')
if [ -n "${LEFTOVERS_FILE-}" ] && [ -n "$left" ]; then
  printf '%s\n' "$left" >> "$LEFTOVERS_FILE"
fi
if [ -n "$left" ]; then
  echo "::error::Left in Azure after the clean-up:"
  printf '%s\n' "$left"
  exit 1
fi
echo "Nothing left in $group, nor tagged elsewhere."
