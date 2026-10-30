#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The sandbox-azure workflow's sweep, the round trip's last step after the
# estate's destroy, whatever happened, and the scheduled clean-up's
# (sandbox-leftovers.yml): deletes what Terraform's state doesn't hold,
# such as a resource whose creation failed half-way, or what a run whose
# destroy never finished left.
#
# The estate's locks first, which would stop the rest, the estate's role
# assignments and custom roles, then every resource in the estate's
# resource group, whatever its kind, a few rounds over, as some can only
# go once what uses them has. The group is the sandbox's own: the
# bootstrap creates it, tagged purpose=infraharvest-sandbox-estate, it
# holds nothing between runs, and the fixture identity can change nothing
# outside it. Without that tag, it deletes only what is tagged
# infraharvest-sandbox or named ih-sbx-. The group itself stays, and so
# do locks, role assignments and custom roles not named ih-sbx-, the
# bootstrap's among them. It fails if anything is left in the group.
#
# The estates' resource types this sweep deletes, or that go with what
# holds them. sandbox-sweep-coverage.sh fails a pull request whose
# estates (e2e/testdata/sandbox/azure*) use a type listed in neither.
# Every resource of the group is swept, whatever its type; these are the
# ones the estates create.
#
# sweep-coverage swept: azapi_resource azurerm_application_security_group azurerm_container_registry
# sweep-coverage swept: azurerm_eventhub_namespace azurerm_key_vault azurerm_lb azurerm_linux_virtual_machine
# sweep-coverage swept: azurerm_log_analytics_workspace azurerm_management_lock azurerm_network_interface
# sweep-coverage swept: azurerm_network_security_group azurerm_postgresql_flexible_server
# sweep-coverage swept: azurerm_private_dns_zone azurerm_private_endpoint azurerm_public_ip
# sweep-coverage swept: azurerm_role_assignment azurerm_role_definition azurerm_route_table
# sweep-coverage swept: azurerm_servicebus_namespace azurerm_storage_account azurerm_user_assigned_identity
# sweep-coverage swept: azurerm_virtual_network
# sweep-coverage with-parent: azurerm_eventhub azurerm_lb_backend_address_pool azurerm_lb_probe azurerm_lb_rule
# sweep-coverage with-parent: azurerm_log_analytics_datasource_windows_event azurerm_network_security_rule
# sweep-coverage with-parent: azurerm_private_dns_zone_virtual_network_link azurerm_route
# sweep-coverage with-parent: azurerm_servicebus_queue azurerm_storage_container azurerm_subnet
# sweep-coverage with-parent: azurerm_subnet_network_security_group_association
# sweep-coverage with-parent: azurerm_subnet_route_table_association
#
# Usage: ARM_SUBSCRIPTION_ID=<id> SANDBOX_AZURE_RESOURCE_GROUP=<group> sandbox-azure-sweep.sh
# Signed in to the Azure CLI as the fixture identity.
set -euo pipefail

sub=(--subscription "${ARM_SUBSCRIPTION_ID:?}")
group=${SANDBOX_AZURE_RESOURCE_GROUP:?}
# The estate's locks alone, named ih-sbx-: one someone else placed on
# the group or a resource in it stays, and keeps what it locks.
locks=$(az lock list "${sub[@]}" -g "$group" --query "[?starts_with(name, 'ih-sbx-')].id" -o tsv)
while IFS= read -r lock; do
  if [ -n "$lock" ]; then
    echo "deleting lock ${lock##*/}"
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
    echo "deleting role assignment ${assignment##*/}"
    az role assignment delete "${sub[@]}" --ids "$assignment" || true
  fi
done <<< "$assignments"
roles=$(az role definition list "${sub[@]}" --custom-role-only true --scope "$scope" --query "[?starts_with(roleName, 'ih-sbx-')].name" -o tsv)
while IFS= read -r role; do
  if [ -n "$role" ]; then
    echo "deleting custom role $role"
    az role definition delete "${sub[@]}" --name "$role" --scope "$scope" || true
  fi
done <<< "$roles"
# Everything in the group when the group is the sandbox's, by its tag;
# else only what is the sandbox's by tag or name.
purpose=$(az group show "${sub[@]}" -n "$group" --query 'tags.purpose' -o tsv)
if [ "$purpose" = infraharvest-sandbox-estate ]; then
  query="[].id"
else
  echo "::warning::$group isn't tagged purpose=infraharvest-sandbox-estate: sweeping only what is tagged infraharvest-sandbox or named ih-sbx-"
  query="[?tags.\"infraharvest-sandbox\" != null || starts_with(name, 'ih-sbx-')].id"
fi
for round in 1 2 3 4 5 6; do
  ids=$(az resource list "${sub[@]}" -g "$group" --query "$query" -o tsv)
  if [ -z "$ids" ]; then
    break
  fi
  if [ "$round" -gt 1 ]; then
    sleep 20
  fi
  while IFS= read -r id; do
    echo "deleting ${id#*/providers/}, round $round"
    az resource delete "${sub[@]}" --ids "$id" > /dev/null || true
  done <<< "$ids"
done
left=$(az resource list "${sub[@]}" -g "$group" --query '[].[type, name]' -o tsv)
if [ -n "$left" ]; then
  echo "::error::Left in $group: $(tr '\t\n' '  ' <<< "$left")"
  exit 1
fi
