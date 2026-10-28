# The resources the query service's end-to-end test creates in floci-az:
# an application security group, a type no lister lists, which the query
# service lists through Terraform's list resources, in a resource group of
# its own. The provider reaches the emulator through the ARM_* variables
# the test sets.

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

resource "azurerm_resource_group" "main" {
  name     = "infraharvest-e2e-query"
  location = "westeurope"
}

resource "azurerm_application_security_group" "web" {
  name                = "infraharvest-e2e-query-web"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
}
