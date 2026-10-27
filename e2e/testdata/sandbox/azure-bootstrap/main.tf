# What the Azure sandbox (the sandbox-azure workflow, TestSandboxAzure in
# e2e/sandbox_azure_test.go) needs in a subscription. Its owner applies it
# once, in Azure Cloud Shell, signed in as an Owner of the subscription
# (see e2e/README.md):
#
# - infraharvest-ci-fixture, a user-assigned managed identity that creates
#   and destroys the estate. It is Contributor on the estate's resource
#   group alone, and may manage the group's management locks, which
#   Contributor can't, and is User Access Administrator on it, for the
#   estate's role assignments and custom role: it can't change anything
#   else in the subscription.
# - infraharvest-ci-readonly, a user-assigned managed identity that
#   imports the estate. permissions/azure, the template users apply,
#   grants it what it grants users: Reader on the whole subscription.
#
# Both trust the GitHub OIDC tokens of the repository's sandbox
# environment, through federated identity credentials: no secret is
# stored, and no app registration (nor the Entra ID rights to make one)
# is needed.

provider "azurerm" {
  features {}
  subscription_id = var.subscription_id

  # What the estate creates. Version 5 of the provider registers nothing
  # by default, and the fixture identity can't register providers itself.
  resource_providers_to_register = [
    "Microsoft.KeyVault",
    "Microsoft.ManagedIdentity",
    "Microsoft.Network",
    "Microsoft.OperationalInsights",
    "Microsoft.Storage",
  ]
}

data "azurerm_client_config" "current" {}

locals {
  github_issuer   = "https://token.actions.githubusercontent.com"
  github_audience = "api://AzureADTokenExchange"
  tags            = { project = "infraharvest-sandbox" }
}

resource "azurerm_resource_group" "identities" {
  name     = var.identity_resource_group
  location = var.location
  tags     = local.tags
}

# The estate's resource group, which the test imports with
# --resource-group. Its tags are part of what the import must keep, which
# the emulator can't show: floci-az drops resource groups' tags.
resource "azurerm_resource_group" "estate" {
  name     = var.estate_resource_group
  location = var.location
  tags     = merge(local.tags, { purpose = "infraharvest-sandbox-estate" })
}

# Creates and destroys the estate.
resource "azurerm_user_assigned_identity" "fixture" {
  name                = "infraharvest-ci-fixture"
  location            = var.location
  resource_group_name = azurerm_resource_group.identities.name
  tags                = local.tags
}

# Imports the estate, with what permissions/azure grants.
resource "azurerm_user_assigned_identity" "readonly" {
  name                = "infraharvest-ci-readonly"
  location            = var.location
  resource_group_name = azurerm_resource_group.identities.name
  tags                = local.tags
}

resource "azurerm_federated_identity_credential" "fixture" {
  name                      = "github-sandbox"
  user_assigned_identity_id = azurerm_user_assigned_identity.fixture.id
  audience                  = [local.github_audience]
  issuer                    = local.github_issuer
  subject                   = var.github_subject
}

resource "azurerm_federated_identity_credential" "readonly" {
  name                      = "github-sandbox"
  user_assigned_identity_id = azurerm_user_assigned_identity.readonly.id
  audience                  = [local.github_audience]
  issuer                    = local.github_issuer
  subject                   = var.github_subject
}

# Contributor on the estate's resource group alone: the estate lives in
# that group, so the fixture can create and delete everything in it, but
# not the group itself, nor anything outside it. Creating resource groups
# would take a role on the whole subscription, which Azure can't limit to
# names under a prefix.
resource "azurerm_role_assignment" "fixture_contributor" {
  scope                = azurerm_resource_group.estate.id
  role_definition_name = "Contributor"
  principal_id         = azurerm_user_assigned_identity.fixture.principal_id
  principal_type       = "ServicePrincipal"
  description          = "infraharvest sandbox: create and destroy the estate."
}

# Contributor can't create or delete management locks
# (Microsoft.Authorization/*/Write and */Delete are among its NotActions),
# and the estate holds one.
resource "azurerm_role_definition" "locks" {
  name        = "infraharvest sandbox lock operator"
  scope       = azurerm_resource_group.estate.id
  description = "Create and delete the management locks of the infraharvest sandbox estate."

  permissions {
    actions = [
      "Microsoft.Authorization/locks/read",
      "Microsoft.Authorization/locks/write",
      "Microsoft.Authorization/locks/delete",
    ]
  }

  assignable_scopes = [azurerm_resource_group.estate.id]
}

resource "azurerm_role_assignment" "fixture_locks" {
  scope              = azurerm_resource_group.estate.id
  role_definition_id = azurerm_role_definition.locks.role_definition_resource_id
  principal_id       = azurerm_user_assigned_identity.fixture.principal_id
  principal_type     = "ServicePrincipal"
  description        = "infraharvest sandbox: create and delete the estate's management lock."
}

# The estate assigns roles to its identity, one of them a custom role it
# defines, which Contributor can't (Microsoft.Authorization/*/Write is
# among its NotActions). Role Based Access Control Administrator can
# assign roles but not define them (Microsoft.Authorization/roleDefinitions/write),
# so the fixture is User Access Administrator, on the estate's resource
# group alone: what it grants and defines is limited to that group.
resource "azurerm_role_assignment" "fixture_access" {
  scope                = azurerm_resource_group.estate.id
  role_definition_name = "User Access Administrator"
  principal_id         = azurerm_user_assigned_identity.fixture.principal_id
  principal_type       = "ServicePrincipal"
  description          = "infraharvest sandbox: assign roles in the estate, and define the custom role it assigns."
}

# The template users apply, as they apply it: the sandbox validates it.
module "readonly" {
  source = "../../../../permissions/azure"

  subscription_id = var.subscription_id
  principal_id    = azurerm_user_assigned_identity.readonly.principal_id
}
