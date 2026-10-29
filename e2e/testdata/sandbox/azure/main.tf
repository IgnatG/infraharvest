# Resources the Azure sandbox test (sandbox_azure_test.go) creates in a
# real subscription before importing them: what floci-az can't cover, such
# as Resource Graph listing, route tables, a storage account, a Key Vault
# and a management lock, the resource group's tags, and a template spec,
# which azurerm has no resource type for, for --azapi, and the kinds of
# resource a team runs: an App Service plan, a PostgreSQL flexible server,
# a container registry, Service Bus and Event Hubs, a load balancer, a
# private endpoint with its private DNS zone, and a virtual machine. It is
# all in the estate's resource group, which the sandbox bootstrap
# (testdata/sandbox/azure-bootstrap) creates, with its tags, and lets the
# fixture identity change, and it is destroyed after each run. Each takes
# the smallest tier there is, with no zone redundancy, replication or
# high availability: together about USD 0.15 an hour (the plan, the
# server, the machine and the load balancer bill by the hour).
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
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
    azapi = {
      source  = "Azure/azapi"
      version = "~> 2.13"
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

variable "drift_tag" {
  description = "A drift tag for the network security group, which a second apply adds out of band of the applied root for infraharvest drift to find."
  type        = string
  default     = ""
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
    # The machine holds nothing: deleted at once, with its OS disk.
    virtual_machine {
      delete_os_disk_on_deletion     = true
      skip_shutdown_and_force_delete = true
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

# The private endpoint's subnet, with no route table or security group.
resource "azurerm_subnet" "endpoints" {
  name                 = "endpoints"
  resource_group_name  = local.rg
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.70.3.0/24"]
}

resource "azurerm_network_security_group" "app" {
  name                = "${local.name}-app"
  location            = local.location
  resource_group_name = local.rg
  tags                = var.drift_tag == "" ? local.tags : merge(local.tags, { drift = var.drift_tag })
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

# A type no lister of its own lists, which the test lists through the
# query service (Terraform's list resources) as well as resource_graph.
# Application security groups are free.
resource "azurerm_application_security_group" "web" {
  name                = "${local.name}-web"
  location            = local.location
  resource_group_name = local.rg
  tags                = local.tags
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

# A template spec, which azurerm has no resource type for (it has a data
# source for template spec versions only), so that the import with --azapi
# brings it in as an azapi_resource. Template specs are free, take tags,
# and live in the resource group; without versions it deploys nothing.
resource "azapi_resource" "template_spec" {
  type      = "Microsoft.Resources/templateSpecs@2022-02-01"
  name      = local.name
  parent_id = data.azurerm_resource_group.sandbox.id
  location  = local.location
  tags      = local.tags

  body = {
    properties = {
      description = "infraharvest sandbox: the import with --azapi must find this template spec."
    }
  }
}

# Role assignments, which the role_assignment service lists from Resource
# Graph, to the identity: a built-in role on the resource group, and a
# custom role that can be assigned only in the group. The import must
# refer each to the identity's principal, and the second to the custom
# role. Neither takes tags: the custom role's name and the assignments'
# descriptions start with the prefix, which the workflow's sweep looks
# for. Creating them takes User Access Administrator on the group, which
# the bootstrap grants the fixture identity.
resource "azurerm_role_assignment" "app_reader" {
  scope                = data.azurerm_resource_group.sandbox.id
  role_definition_name = "Reader"
  principal_id         = azurerm_user_assigned_identity.app.principal_id
  principal_type       = "ServicePrincipal"
  description          = "${local.name}: the import must refer this to the identity."
}

resource "azurerm_role_definition" "app" {
  name        = "${local.name}-identity-reader"
  scope       = data.azurerm_resource_group.sandbox.id
  description = "${local.name}: a custom role the import must list, as it can be assigned in the estate's group alone."

  permissions {
    actions = ["Microsoft.ManagedIdentity/userAssignedIdentities/read"]
  }

  assignable_scopes = [data.azurerm_resource_group.sandbox.id]
}

resource "azurerm_role_assignment" "app_custom" {
  scope              = data.azurerm_resource_group.sandbox.id
  role_definition_id = azurerm_role_definition.app.role_definition_resource_id
  principal_id       = azurerm_user_assigned_identity.app.principal_id
  principal_type     = "ServicePrincipal"
  description        = "${local.name}: the import must refer this to the identity and the custom role."
}

# An App Service plan, Linux on B1, the smallest that runs apps all day,
# with one worker. No app runs on it: azurerm reads a web or function
# app's settings and publishing credentials, which the Reader role
# permissions/azure grants can't list.
resource "azurerm_service_plan" "main" {
  name                = local.name
  location            = local.location
  resource_group_name = local.rg
  os_type             = "Linux"
  sku_name            = "B1"
  worker_count        = 1
  tags                = local.tags
}

# A PostgreSQL flexible server on the smallest burstable size and storage,
# with no high availability, geo-redundant backup or public access, and
# the shortest backup retention. Its administrator password comes from
# random_password: Azure never returns it, so the import must leave it
# out, and the test checks no file of the output holds it.
resource "random_password" "postgres" {
  length           = 24
  min_lower        = 1
  min_upper        = 1
  min_numeric      = 1
  min_special      = 1
  override_special = "!#%*-_"
}

resource "azurerm_postgresql_flexible_server" "main" {
  name                          = local.name
  location                      = local.location
  resource_group_name           = local.rg
  version                       = "16"
  sku_name                      = "B_Standard_B1ms"
  storage_mb                    = 32768
  storage_tier                  = "P4"
  backup_retention_days         = 7
  geo_redundant_backup_enabled  = false
  public_network_access_enabled = false
  administrator_login           = "ihsbxadmin"
  administrator_password        = random_password.postgres.result
  tags                          = local.tags
}

# A container registry on Basic, without the admin user, whose
# credentials the import would have to list.
resource "azurerm_container_registry" "main" {
  name                = local.compact
  location            = local.location
  resource_group_name = local.rg
  sku                 = "Basic"
  admin_enabled       = false
  tags                = local.tags
}

# A Service Bus namespace on Basic with a queue, and an Event Hubs
# namespace on Basic, with one throughput unit, with an event hub of one
# partition that keeps events a day. Each namespace has the
# RootManageSharedAccessKey rule Azure creates, and the event hub the
# $Default consumer group, which the default selection leaves out.
resource "azurerm_servicebus_namespace" "main" {
  name                = local.name
  location            = local.location
  resource_group_name = local.rg
  sku                 = "Basic"
  tags                = local.tags
}

resource "azurerm_servicebus_queue" "orders" {
  name         = "orders"
  namespace_id = azurerm_servicebus_namespace.main.id
}

resource "azurerm_eventhub_namespace" "main" {
  name                = local.name
  location            = local.location
  resource_group_name = local.rg
  sku                 = "Basic"
  capacity            = 1
  tags                = local.tags
}

resource "azurerm_eventhub" "events" {
  name              = "events"
  namespace_id      = azurerm_eventhub_namespace.main.id
  partition_count   = 1
  message_retention = 1
}

# A Standard load balancer on a Standard public IP, with a backend pool,
# a probe and a rule. Nothing is in the pool.
resource "azurerm_public_ip" "lb" {
  name                = "${local.name}-lb"
  location            = local.location
  resource_group_name = local.rg
  sku                 = "Standard"
  allocation_method   = "Static"
  tags                = local.tags
}

resource "azurerm_lb" "main" {
  name                = local.name
  location            = local.location
  resource_group_name = local.rg
  sku                 = "Standard"
  tags                = local.tags

  frontend_ip_configuration {
    name                 = "public"
    public_ip_address_id = azurerm_public_ip.lb.id
  }
}

resource "azurerm_lb_backend_address_pool" "app" {
  name            = "app"
  loadbalancer_id = azurerm_lb.main.id
}

resource "azurerm_lb_probe" "http" {
  name            = "http"
  loadbalancer_id = azurerm_lb.main.id
  protocol        = "Tcp"
  port            = 80
}

resource "azurerm_lb_rule" "http" {
  name                           = "http"
  loadbalancer_id                = azurerm_lb.main.id
  protocol                       = "Tcp"
  frontend_port                  = 80
  backend_port                   = 80
  frontend_ip_configuration_name = "public"
  backend_address_pool_ids       = [azurerm_lb_backend_address_pool.app.id]
  probe_id                       = azurerm_lb_probe.http.id
  disable_outbound_snat          = true
}

# A private endpoint to the storage account's blobs, in its own subnet,
# with the private DNS zone it registers in, linked to the network. Its
# network interface, which Azure creates and deletes with it, is named
# after the prefix, so that a sweep finds it.
resource "azurerm_private_dns_zone" "blob" {
  name                = "privatelink.blob.core.windows.net"
  resource_group_name = local.rg
  tags                = local.tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "blob" {
  name                = local.name
  private_dns_zone_id = azurerm_private_dns_zone.blob.id
  virtual_network_id  = azurerm_virtual_network.main.id
  tags                = local.tags
}

resource "azurerm_private_endpoint" "blob" {
  name                          = "${local.name}-blob"
  location                      = local.location
  resource_group_name           = local.rg
  subnet_id                     = azurerm_subnet.endpoints.id
  custom_network_interface_name = "${local.name}-blob-nic"
  tags                          = local.tags

  private_service_connection {
    name                           = "blob"
    private_connection_resource_id = azurerm_storage_account.main.id
    subresource_names              = ["blob"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "blob"
    private_dns_zone_ids = [azurerm_private_dns_zone.blob.id]
  }
}

# A Linux virtual machine of the smallest B size, in the app subnet,
# with no public IP, signed in to with an SSH key from tls_private_key
# alone. Its OS disk, which the machine creates and deletes with itself,
# takes no tags: it is named after the prefix, so that a sweep finds it.
resource "tls_private_key" "vm" {
  algorithm = "RSA"
  rsa_bits  = 4096
}

resource "azurerm_network_interface" "vm" {
  name                = "${local.name}-vm"
  location            = local.location
  resource_group_name = local.rg
  tags                = local.tags

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.app.id
    private_ip_address_allocation = "Dynamic"
  }
}

resource "azurerm_linux_virtual_machine" "main" {
  name                  = "${local.name}-vm"
  location              = local.location
  resource_group_name   = local.rg
  size                  = "Standard_B1s"
  admin_username        = "ihsbx"
  network_interface_ids = [azurerm_network_interface.vm.id]
  tags                  = local.tags

  admin_ssh_key {
    username   = "ihsbx"
    public_key = tls_private_key.vm.public_key_openssh
  }

  os_disk {
    name                 = "${local.name}-os"
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "ubuntu-24_04-lts"
    sku       = "server"
    version   = "latest"
  }
}
