# Resources the Azure environments sandbox test
# (TestSandboxAzureEnvironments, in sandbox_azure_environments_test.go)
# creates in two real subscriptions of one tenant before importing them:
# a network security group of dev in the first sandbox subscription and
# one of prd in the second, each in its subscription's estate resource
# group, which the bootstraps create under the same name, as the
# configuration file's environments map them by subscription. Their owner
# tag differs. prod_api adds prd's api group, for the incremental import,
# and prod_owner changes prd's owner tag, for drift. Each subscription's
# fixture identity creates what is in it, as only it may. Network
# security groups cost nothing, and the estate is destroyed after each
# run.
# Both providers sign in through the ARM_* variables the test sets, each
# as the client ID it names.

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.0"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same resource groups, such as one per engine."
  type        = string
  default     = "terraform"

  validation {
    condition     = can(regex("^[a-z0-9]{1,9}$", var.suffix))
    error_message = "Use up to nine lower-case letters and digits."
  }
}

variable "resource_group" {
  description = "The estate's resource group, which each subscription's bootstrap creates under this name."
  type        = string
  default     = "infraharvest-sandbox"
}

variable "dev_subscription_id" {
  description = "The first sandbox subscription, dev's."
  type        = string
}

variable "dev_client_id" {
  description = "The client ID of the first subscription's fixture identity."
  type        = string
}

variable "prod_subscription_id" {
  description = "The second sandbox subscription, prd's."
  type        = string
}

variable "prod_client_id" {
  description = "The client ID of the second subscription's fixture identity."
  type        = string
}

variable "prod_api" {
  description = "Whether prd has an api group, which the test creates once the merged root is applied, for prod's incremental import."
  type        = bool
  default     = false
}

variable "prod_owner" {
  description = "The owner tag of prd's web group, which the test changes out of band of the applied merged root, for drift."
  type        = string
  default     = "team-b"
}

provider "azurerm" {
  alias           = "dev"
  subscription_id = var.dev_subscription_id
  client_id       = var.dev_client_id
  features {}
}

provider "azurerm" {
  alias           = "prod"
  subscription_id = var.prod_subscription_id
  client_id       = var.prod_client_id
  features {}
}

data "azurerm_resource_group" "dev" {
  provider = azurerm.dev
  name     = var.resource_group
}

data "azurerm_resource_group" "prod" {
  provider = azurerm.prod
  name     = var.resource_group
}

locals {
  # Everything a run creates is named after the prefix and carries the
  # tag, so a sweep finds what a run left.
  name = "ih-sbx-${var.suffix}"
  tags = { project = "infraharvest-sandbox", infraharvest-sandbox = var.suffix }
}

resource "azurerm_network_security_group" "dev" {
  provider            = azurerm.dev
  name                = "${local.name}-dev-web"
  location            = data.azurerm_resource_group.dev.location
  resource_group_name = data.azurerm_resource_group.dev.name
  tags                = merge(local.tags, { owner = "team-a" })
}

resource "azurerm_network_security_group" "prod" {
  provider            = azurerm.prod
  name                = "${local.name}-prd-web"
  location            = data.azurerm_resource_group.prod.location
  resource_group_name = data.azurerm_resource_group.prod.name
  tags                = merge(local.tags, { owner = var.prod_owner })
}

resource "azurerm_network_security_group" "prod_api" {
  count               = var.prod_api ? 1 : 0
  provider            = azurerm.prod
  name                = "${local.name}-prd-api"
  location            = data.azurerm_resource_group.prod.location
  resource_group_name = data.azurerm_resource_group.prod.name
  tags                = local.tags
}
