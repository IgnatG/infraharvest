# azurerm_container_registry_scope_map, from data/azurerm/azurerm.yaml

import {
  to = azurerm_container_registry_scope_map.ci_push
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ContainerRegistry/registries/acrapp/scopeMaps/ci-push"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ContainerRegistry/registries/acrapp/scopeMaps/_repositories_pull: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ContainerRegistry/registries/acrapp/scopeMaps/_repositories_pull": a scope map Azure creates with every container registry (_repositories_admin, _repositories_pull, ...), which can't be changed or deleted
