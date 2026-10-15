resource "azurerm_virtual_network" "vnet_hub" {
  address_space                  = ["10.70.0.0/16"]
  bgp_community                  = null
  dns_servers                    = []
  edge_zone                      = null
  location                       = "uksouth"
  name                           = "vnet-hub"
  private_endpoint_vnet_policies = "Disabled"
  resource_group_name            = "rg-network"
  subnet = [{
    address_prefixes                              = ["10.70.1.0/24"]
    default_outbound_access_enabled               = true
    delegation                                    = []
    id                                            = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/virtualNetworks/vnet-hub/subnets/app"
    name                                          = "app"
    private_endpoint_network_policies             = "Disabled"
    private_link_service_network_policies_enabled = true
    route_table_id                                = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/routeTables/rt-app"
    security_group                                = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/networkSecurityGroups/nsg-app"
    service_endpoint_policy_ids                   = []
    service_endpoints                             = []
  }]
  tags = {
    project = "network"
  }
}
