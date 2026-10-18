# An example: not the module's, and never read.

resource "azurerm_resource_group" "example" {
  location = "westeurope"
  name     = "example"
}

module "identity" {
  source = "../.."

  location            = azurerm_resource_group.example.location
  name                = "example"
  resource_group_name = azurerm_resource_group.example.name
}
