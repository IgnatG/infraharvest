# azapi_resource, from data/azapi/azapi.yaml

import {
  to = azapi_resource.batch
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.AzureFleet/fleets/batch?api-version=2024-11-01"
}

import {
  to = azapi_resource.surface
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Easm/workspaces/surface"
}

# Left out where the plan stays the same, as the default: ignore_casing=false, ignore_missing_property=true, ignore_null_property=false, schema_validation_enabled=true
