# Resources the end-to-end test of --regenerate's removed blocks for
# Azure (moves_azure_test.go) creates in floci-az, in a resource group of
# their own, which no other test lists. Applied again with handed_env=prod,
# the first network security group gets another environment, which puts
# it in another root; the others stay as they are.

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.0"
    }
  }
}

provider "azurerm" {
  features {}
}

variable "handed_env" {
  type    = string
  default = "dev"
}

locals {
  name     = "infraharvest-e2e-moves"
  location = "westeurope"
}

resource "azurerm_resource_group" "moves" {
  name     = local.name
  location = local.location
}

resource "azurerm_network_security_group" "handed" {
  name                = "${local.name}-handed"
  location            = local.location
  resource_group_name = azurerm_resource_group.moves.name
  tags                = { env = var.handed_env }
}

# Groups that stay as they are, so that the root keeps more than one and
# labels them by name, and keeps their addresses.
resource "azurerm_network_security_group" "kept" {
  name                = "${local.name}-kept"
  location            = local.location
  resource_group_name = azurerm_resource_group.moves.name
  tags                = { env = "dev" }
}

resource "azurerm_network_security_group" "steady" {
  name                = "${local.name}-steady"
  location            = local.location
  resource_group_name = azurerm_resource_group.moves.name
  tags                = { env = "dev" }
}
