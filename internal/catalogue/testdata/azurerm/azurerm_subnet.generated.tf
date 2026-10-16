resource "azurerm_subnet" "app" {
  address_prefixes                              = ["10.70.1.0/24"]
  default_outbound_access_enabled               = true
  name                                          = "app"
  private_endpoint_network_policies             = "Disabled"
  private_link_service_network_policies_enabled = true
  resource_group_name                           = "rg-network"
  service_endpoint_policy_ids                   = []
  sharing_scope                                 = null
  virtual_network_name                          = "vnet-hub"
}

resource "azurerm_subnet" "private" {
  address_prefixes                              = ["10.70.2.0/24"]
  default_outbound_access_enabled               = false
  name                                          = "private"
  private_endpoint_network_policies             = "Enabled"
  private_link_service_network_policies_enabled = false
  resource_group_name                           = "rg-network"
  service_endpoint_policy_ids                   = []
  sharing_scope                                 = null
  virtual_network_name                          = "vnet-hub"
}
