# A module's required providers in each form: with a source and a
# version, with configuration_aliases, without a version, and the old
# version string.

terraform {
  required_version = ">= 1.9, < 2.0"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 4.60.0, < 5.0.0"
    }
    azapi = {
      source                = "Azure/azapi"
      version               = "~> 2.12"
      configuration_aliases = [azapi.alt]
    }
    random = {
      source = "hashicorp/random"
    }
    google = "~> 7.0"
  }
}
