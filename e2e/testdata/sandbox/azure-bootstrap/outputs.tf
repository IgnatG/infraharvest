output "tenant_id" {
  description = "The tenant of the identities: AZURE_TENANT_ID."
  value       = data.azurerm_client_config.current.tenant_id
}

output "subscription_id" {
  description = "The sandbox subscription: AZURE_SUBSCRIPTION_ID."
  value       = var.subscription_id
}

output "fixture_client_id" {
  description = "The client ID of infraharvest-ci-fixture: AZURE_FIXTURE_CLIENT_ID."
  value       = azurerm_user_assigned_identity.fixture.client_id
}

output "readonly_client_id" {
  description = "The client ID of infraharvest-ci-readonly: AZURE_READONLY_CLIENT_ID."
  value       = azurerm_user_assigned_identity.readonly.client_id
}

output "estate_resource_group" {
  description = "The resource group the estate is created in: AZURE_RESOURCE_GROUP."
  value       = azurerm_resource_group.estate.name
}

output "gh_commands" {
  description = "Set the variables of the repository's sandbox environment that the sandbox-azure workflow reads. Run them where gh is signed in to GitHub."
  value       = <<-EOT
    gh variable set AZURE_TENANT_ID --env sandbox --repo ${var.github_repository} --body ${data.azurerm_client_config.current.tenant_id}
    gh variable set AZURE_SUBSCRIPTION_ID --env sandbox --repo ${var.github_repository} --body ${var.subscription_id}
    gh variable set AZURE_FIXTURE_CLIENT_ID --env sandbox --repo ${var.github_repository} --body ${azurerm_user_assigned_identity.fixture.client_id}
    gh variable set AZURE_READONLY_CLIENT_ID --env sandbox --repo ${var.github_repository} --body ${azurerm_user_assigned_identity.readonly.client_id}
    gh variable set AZURE_RESOURCE_GROUP --env sandbox --repo ${var.github_repository} --body ${azurerm_resource_group.estate.name}
  EOT
}
