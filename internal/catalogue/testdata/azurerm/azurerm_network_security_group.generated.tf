resource "azurerm_network_security_group" "nsg_app" {
  location            = "uksouth"
  name                = "nsg-app"
  resource_group_name = "rg-network"
  security_rule = [{
    access                                     = "Allow"
    description                                = ""
    destination_address_prefix                 = "*"
    destination_address_prefixes               = []
    destination_application_security_group_ids = []
    destination_port_range                     = "443"
    destination_port_ranges                    = []
    direction                                  = "Inbound"
    name                                       = "https"
    priority                                   = 100
    protocol                                   = "Tcp"
    source_address_prefix                      = "VirtualNetwork"
    source_address_prefixes                    = []
    source_application_security_group_ids      = []
    source_port_range                          = "*"
    source_port_ranges                         = []
  }]
  tags = {
    project = "network"
  }
}
