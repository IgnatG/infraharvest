output "project_id" {
  description = "The sandbox project: GOOGLE_PROJECT_ID."
  value       = var.project_id
}

output "region" {
  description = "Where the estate's regional resources are created: GOOGLE_REGION."
  value       = var.region
}

output "workload_identity_provider" {
  description = "The workload identity provider's full name, which the workflow signs in through: GOOGLE_WORKLOAD_IDENTITY_PROVIDER."
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "fixture_service_account" {
  description = "The email of infraharvest-ci-fixture: GOOGLE_FIXTURE_SERVICE_ACCOUNT."
  value       = google_service_account.fixture.email
}

output "readonly_service_account" {
  description = "The email of infraharvest-ci-readonly: GOOGLE_READONLY_SERVICE_ACCOUNT."
  value       = google_service_account.readonly.email
}

output "readonly_roles" {
  description = "What permissions/google grants infraharvest-ci-readonly on the project."
  value       = module.readonly.granted_roles
}

output "gh_commands" {
  description = "Set the variables of the repository's sandbox environment that the sandbox-google workflow reads. Run them where gh is signed in to GitHub."
  value       = <<-EOT
    gh variable set GOOGLE_PROJECT_ID --env sandbox --repo ${var.github_repository} --body ${var.project_id}
    gh variable set GOOGLE_REGION --env sandbox --repo ${var.github_repository} --body ${var.region}
    gh variable set GOOGLE_WORKLOAD_IDENTITY_PROVIDER --env sandbox --repo ${var.github_repository} --body ${google_iam_workload_identity_pool_provider.github.name}
    gh variable set GOOGLE_FIXTURE_SERVICE_ACCOUNT --env sandbox --repo ${var.github_repository} --body ${google_service_account.fixture.email}
    gh variable set GOOGLE_READONLY_SERVICE_ACCOUNT --env sandbox --repo ${var.github_repository} --body ${google_service_account.readonly.email}
  EOT
}
