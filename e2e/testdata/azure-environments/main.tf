# Resources the end-to-end test of merging Azure environments
# (environments_azure_test.go) creates in floci-az: a resource group of
# the same name in a dev and a prod subscription, as the configuration
# file's environments map them, and a network security group of each
# environment in it, whose owner tag differs between the two. Each
# environment's api group is created only later: prod's for the
# incremental import, dev's for --regenerate. The provider reaches the
# emulator through the ARM_* variables the test sets.

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.0"
    }
  }
}

provider "azurerm" {
  alias           = "dev"
  subscription_id = "00000000-0000-0000-0000-000000000001"
  features {}
}

provider "azurerm" {
  alias           = "prod"
  subscription_id = "00000000-0000-0000-0000-000000000003"
  features {}
}

variable "dev_owner" {
  description = "The owner tag of dev's web group, which the test changes once the merged root is applied, for drift."
  type        = string
  default     = "team-a"
}

variable "prod_api" {
  description = "Whether prod has an api group, which the test creates after the merged root is applied."
  type        = bool
  default     = false
}

variable "dev_api" {
  description = "Whether dev has an api group too, which the test creates before --regenerate."
  type        = bool
  default     = false
}

resource "azurerm_resource_group" "dev" {
  provider = azurerm.dev
  name     = "infraharvest-e2e-envs"
  location = "westeurope"
}

resource "azurerm_resource_group" "prod" {
  provider = azurerm.prod
  name     = "infraharvest-e2e-envs"
  location = "westeurope"
}

resource "azurerm_network_security_group" "dev" {
  provider            = azurerm.dev
  name                = "shop-dev-web"
  location            = azurerm_resource_group.dev.location
  resource_group_name = azurerm_resource_group.dev.name
  tags                = { owner = var.dev_owner }
}

resource "azurerm_network_security_group" "prod" {
  provider            = azurerm.prod
  name                = "shop-prd-web"
  location            = azurerm_resource_group.prod.location
  resource_group_name = azurerm_resource_group.prod.name
  tags                = { owner = "team-b" }
}

resource "azurerm_network_security_group" "dev_api" {
  count               = var.dev_api ? 1 : 0
  provider            = azurerm.dev
  name                = "shop-dev-api"
  location            = azurerm_resource_group.dev.location
  resource_group_name = azurerm_resource_group.dev.name
}

resource "azurerm_network_security_group" "prod_api" {
  count               = var.prod_api ? 1 : 0
  provider            = azurerm.prod
  name                = "shop-prd-api"
  location            = azurerm_resource_group.prod.location
  resource_group_name = azurerm_resource_group.prod.name
}
