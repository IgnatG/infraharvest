# Resources the Azure sandbox test (sandbox_azure_test.go) creates in a
# real subscription before importing them: what floci-az can't cover, such
# as Resource Graph listing, route tables, a storage account, a Key Vault
# and a management lock, and the resource group's tags. It is all in the
# estate's resource group, which the sandbox bootstrap
# (testdata/sandbox/azure-bootstrap) creates, with its tags, and lets the
# fixture identity change: nothing bills by the hour, and it is destroyed
# after each run.
# The provider signs in through the ARM_* variables the test sets.

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

variable "suffix" {
  description = "Tells apart the estates of runs in the same resource group, such as one per engine."
  type        = string
  default     = "terraform"

  validation {
    condition     = can(regex("^[a-z0-9]{1,9}$", var.suffix))
    error_message = "Use up to nine lower-case letters and digits: the suffix goes into the storage account's name."
  }
}

variable "resource_group" {
  description = "The estate's resource group, which the bootstrap creates."
  type        = string
  default     = "infraharvest-sandbox"
}

provider "azurerm" {
  features {
    # The fixture identity's role is on the resource group alone, and the
    # deleted vaults are the subscription's: it can neither purge the
    # vault nor look for a deleted one to recover. A deleted vault is
    # kept for soft_delete_retention_days, at no cost, under a name no
    # other run uses.
    key_vault {
      purge_soft_delete_on_destroy    = false
      recover_soft_deleted_key_vaults = false
    }
    # Deleted, not kept for 14 days as a soft-deleted workspace.
    log_analytics_workspace {
      permanently_delete_on_destroy = true
    }
  }
}

data "azurerm_client_config" "current" {}

data "azurerm_resource_group" "sandbox" {
  name = var.resource_group
}

# Names no other run has used, as Key Vault and storage account names must
# be unique across Azure, and a deleted vault keeps its name for a while.
resource "random_string" "id" {
  length  = 6
  upper   = false
  special = false
}

locals {
  # Everything a run creates is named after the prefix and carries the
  # tag, so a sweep finds what a run left.
  name     = "ih-sbx-${var.suffix}-${random_string.id.result}"
  compact  = "ihsbx${var.suffix}${random_string.id.result}"
  location = data.azurerm_resource_group.sandbox.location
  rg       = data.azurerm_resource_group.sandbox.name
  tags     = { project = "infraharvest-sandbox", infraharvest-sandbox = var.suffix }
}

resource "azurerm_virtual_network" "main" {
  name                = local.name
  location            = local.location
  resource_group_name = local.rg
  address_space       = ["10.70.0.0/16"]
  tags                = local.tags
}

resource "azurerm_subnet" "app" {
  name                 = "app"
  resource_group_name  = local.rg
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.70.1.0/24"]
}

resource "azurerm_subnet" "data" {
  name                 = "data"
  resource_group_name  = local.rg
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.70.2.0/24"]
}

resource "azurerm_network_security_group" "app" {
  name                = "${local.name}-app"
  location            = local.location
  resource_group_name = local.rg
  tags                = local.tags
}

resource "azurerm_network_security_rule" "https" {
  name                        = "https"
  resource_group_name         = local.rg
  network_security_group_name = azurerm_network_security_group.app.name
  priority                    = 100
  direction                   = "Inbound"
  access                      = "Allow"
  protocol                    = "Tcp"
  source_port_range           = "*"
  destination_port_range      = "443"
  source_address_prefix       = "VirtualNetwork"
  destination_address_prefix  = "*"
}

resource "azurerm_subnet_network_security_group_association" "app" {
  subnet_id                 = azurerm_subnet.app.id
  network_security_group_id = azurerm_network_security_group.app.id
}

# A route table, which floci-az doesn't serve, with a route of its own,
# on both subnets.
resource "azurerm_route_table" "main" {
  name                = local.name
  location            = local.location
  resource_group_name = local.rg
  tags                = local.tags
}

resource "azurerm_route" "internet" {
  name                = "internet"
  resource_group_name = local.rg
  route_table_name    = azurerm_route_table.main.name
  address_prefix      = "0.0.0.0/0"
  next_hop_type       = "Internet"
}

resource "azurerm_subnet_route_table_association" "app" {
  subnet_id      = azurerm_subnet.app.id
  route_table_id = azurerm_route_table.main.id
}

resource "azurerm_subnet_route_table_association" "data" {
  subnet_id      = azurerm_subnet.data.id
  route_table_id = azurerm_route_table.main.id
}

# A storage account with a container: an empty account costs nothing.
resource "azurerm_storage_account" "main" {
  name                     = local.compact
  location                 = local.location
  resource_group_name      = local.rg
  account_tier             = "Standard"
  account_replication_type = "LRS"
  min_tls_version          = "TLS1_2"
  tags                     = local.tags
}

resource "azurerm_storage_container" "data" {
  name                  = "data"
  storage_account_id    = azurerm_storage_account.main.id
  container_access_type = "private"
}

# A Key Vault that authorises data access through Azure RBAC, with purge
# protection off and the shortest soft-delete retention, so that it can be
# deleted. It holds nothing: the import reads its configuration only.
resource "azurerm_key_vault" "main" {
  name                       = local.name
  location                   = local.location
  resource_group_name        = local.rg
  tenant_id                  = data.azurerm_client_config.current.tenant_id
  sku_name                   = "standard"
  rbac_authorization_enabled = true
  purge_protection_enabled   = false
  soft_delete_retention_days = 7
  tags                       = local.tags
}

# Pay as you go, and nothing is sent to it.
resource "azurerm_log_analytics_workspace" "main" {
  name                = local.name
  location            = local.location
  resource_group_name = local.rg
  sku                 = "PerGB2018"
  retention_in_days   = 30
  tags                = local.tags
}

# A workspace data source, which infraharvest lists by kind.
resource "azurerm_log_analytics_datasource_windows_event" "application" {
  name                = "${local.name}-application"
  resource_group_name = local.rg
  workspace_name      = azurerm_log_analytics_workspace.main.name
  event_log_name      = "Application"
  event_types         = ["Error"]
}

resource "azurerm_user_assigned_identity" "app" {
  name                = local.name
  location            = local.location
  resource_group_name = local.rg
  tags                = local.tags
}

# A management lock, which the management_lock service lists: on the
# identity, which nothing else depends on. Its scope is the identity's ID,
# so Terraform deletes it before the identity.
resource "azurerm_management_lock" "app" {
  name       = "${local.name}-keep"
  scope      = azurerm_user_assigned_identity.app.id
  lock_level = "CanNotDelete"
  notes      = "infraharvest sandbox: the import must find this lock."
}
