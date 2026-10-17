# A resource the Azure selection test adds to what testdata/azure creates
# in floci-az: a network security group whose tag says Pulumi manages it,
# which the default selection leaves out, for the test to include in the
# selection file.

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

resource "azurerm_network_security_group" "pulumi" {
  name                = "infraharvest-e2e-pulumi"
  location            = "westeurope"
  resource_group_name = "infraharvest-e2e"
  tags = {
    project    = "infraharvest-e2e"
    env        = "test"
    managed-by = "Pulumi"
  }
}
