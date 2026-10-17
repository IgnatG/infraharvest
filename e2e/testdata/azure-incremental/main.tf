# A resource the Azure incremental test adds to what testdata/azure creates
# in floci-az once the first import is done, for --incremental to add to
# the root.

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

resource "azurerm_network_security_group" "added" {
  name                = "infraharvest-e2e-added"
  location            = "westeurope"
  resource_group_name = "infraharvest-e2e"
  tags = {
    project = "infraharvest-e2e"
    env     = "test"
  }
}
