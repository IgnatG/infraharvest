# Resources the Azure end-to-end test creates in floci-az before importing
# them. The provider reaches the emulator through the ARM_* variables the
# test sets (ARM_ENVIRONMENT=stack and ARM_METADATA_HOSTNAME), like
# infraharvest's output.

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  features {}
}

locals {
  name     = "infraharvest-e2e"
  location = "westeurope"
}

resource "azurerm_resource_group" "main" {
  name     = local.name
  location = local.location
}

resource "azurerm_virtual_network" "main" {
  name                = "${local.name}-vnet"
  location            = local.location
  resource_group_name = azurerm_resource_group.main.name
  address_space       = ["10.20.0.0/16"]
}

resource "azurerm_subnet" "app" {
  name                 = "app"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.20.1.0/24"]
}

resource "azurerm_network_security_group" "app" {
  name                = "${local.name}-app"
  location            = local.location
  resource_group_name = azurerm_resource_group.main.name
}

resource "azurerm_network_security_rule" "https" {
  name                        = "https"
  resource_group_name         = azurerm_resource_group.main.name
  network_security_group_name = azurerm_network_security_group.app.name
  priority                    = 100
  direction                   = "Inbound"
  access                      = "Allow"
  protocol                    = "Tcp"
  source_port_range           = "*"
  destination_port_range      = "443"
  source_address_prefix       = "*"
  destination_address_prefix  = "*"
}

resource "azurerm_public_ip" "app" {
  name                = "${local.name}-app"
  location            = local.location
  resource_group_name = azurerm_resource_group.main.name
  allocation_method   = "Static"
  sku                 = "Standard"
}

resource "azurerm_network_interface" "app" {
  name                = "${local.name}-app"
  location            = local.location
  resource_group_name = azurerm_resource_group.main.name

  ip_configuration {
    name                          = "primary"
    subnet_id                     = azurerm_subnet.app.id
    private_ip_address_allocation = "Dynamic"
    public_ip_address_id          = azurerm_public_ip.app.id
  }
}
