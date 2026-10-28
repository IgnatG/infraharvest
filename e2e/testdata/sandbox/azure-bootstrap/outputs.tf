locals {
  # The second subscription's variables are AZURE_SECOND_*; it shares the
  # first's tenant.
  variable_prefix = var.slot == "" ? "AZURE_" : "AZURE_SECOND_"
}

output "tenant_id" {
  description = "The tenant of the identities: AZURE_TENANT_ID, which both subscriptions share."
  value       = data.azurerm_client_config.current.tenant_id
}

output "subscription_id" {
  description = "The sandbox subscription: AZURE_SUBSCRIPTION_ID, or AZURE_SECOND_SUBSCRIPTION_ID for the second."
  value       = var.subscription_id
}

output "fixture_client_id" {
  description = "The client ID of infraharvest-ci-fixture: AZURE_FIXTURE_CLIENT_ID, or AZURE_SECOND_FIXTURE_CLIENT_ID for the second subscription."
  value       = azurerm_user_assigned_identity.fixture.client_id
}

output "readonly_client_id" {
  description = "The client ID of infraharvest-ci-readonly: AZURE_READONLY_CLIENT_ID, or AZURE_SECOND_READONLY_CLIENT_ID for the second subscription."
  value       = azurerm_user_assigned_identity.readonly.client_id
}

output "readonly_principal_id" {
  description = "The object ID of infraharvest-ci-readonly: the first subscription's is the second's bootstrap's first_readonly_principal_id."
  value       = azurerm_user_assigned_identity.readonly.principal_id
}

output "estate_resource_group" {
  description = "The resource group the estate is created in: AZURE_RESOURCE_GROUP, or AZURE_SECOND_RESOURCE_GROUP for the second subscription."
  value       = azurerm_resource_group.estate.name
}

output "gh_commands" {
  description = "Set the variables of the repository's sandbox environment that the sandbox-azure workflow reads. Run them where gh is signed in to GitHub."
  value = join("", [
    var.slot == "" ? "gh variable set AZURE_TENANT_ID --env sandbox --repo ${var.github_repository} --body ${data.azurerm_client_config.current.tenant_id}\n" : "",
    <<-EOT
    gh variable set ${local.variable_prefix}SUBSCRIPTION_ID --env sandbox --repo ${var.github_repository} --body ${var.subscription_id}
    gh variable set ${local.variable_prefix}FIXTURE_CLIENT_ID --env sandbox --repo ${var.github_repository} --body ${azurerm_user_assigned_identity.fixture.client_id}
    gh variable set ${local.variable_prefix}READONLY_CLIENT_ID --env sandbox --repo ${var.github_repository} --body ${azurerm_user_assigned_identity.readonly.client_id}
    gh variable set ${local.variable_prefix}RESOURCE_GROUP --env sandbox --repo ${var.github_repository} --body ${azurerm_resource_group.estate.name}
    EOT
  ])
}
