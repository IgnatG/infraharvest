resource "azurerm_route_table" "rt_app" {
  bgp_route_propagation_enabled = true
  location                      = "uksouth"
  name                          = "rt-app"
  resource_group_name           = "rg-network"
  route = [{
    address_prefix         = "0.0.0.0/0"
    name                   = "internet"
    next_hop_in_ip_address = ""
    next_hop_type          = "Internet"
    }, {
    address_prefix         = "10.0.0.0/8"
    name                   = "firewall"
    next_hop_in_ip_address = "10.70.0.4"
    next_hop_type          = "VirtualAppliance"
  }]
  tags = {
    project = "network"
  }
}

resource "azurerm_route_table" "rt_firewall" {
  bgp_route_propagation_enabled = false
  location                      = "uksouth"
  name                          = "rt-firewall"
  resource_group_name           = "rg-network"
  route                         = []
}
