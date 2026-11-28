output "project_id" {
  description = "The sandbox project: GOOGLE_PROJECT_ID, or GOOGLE_SECOND_PROJECT_ID for the second."
  value       = var.project_id
}

output "region" {
  description = "Where the estate's regional resources are created: GOOGLE_REGION."
  value       = var.region
}

output "workload_identity_provider" {
  description = "The workload identity provider's full name, which the workflow signs in through: GOOGLE_WORKLOAD_IDENTITY_PROVIDER, or GOOGLE_SECOND_WORKLOAD_IDENTITY_PROVIDER for the second project."
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "fixture_service_account" {
  description = "The email of infraharvest-ci-fixture: GOOGLE_FIXTURE_SERVICE_ACCOUNT, or GOOGLE_SECOND_FIXTURE_SERVICE_ACCOUNT for the second project."
  value       = google_service_account.fixture.email
}

output "function_service_account" {
  description = "The email of infraharvest-ci-function, which the estate builds and runs its function as, by this name."
  value       = google_service_account.function.email
}

output "readonly_service_account" {
  description = "The email of infraharvest-ci-readonly: GOOGLE_READONLY_SERVICE_ACCOUNT, or GOOGLE_SECOND_READONLY_SERVICE_ACCOUNT for the second project. The first project's is the second's bootstrap's first_readonly_service_account."
  value       = google_service_account.readonly.email
}

output "readonly_roles" {
  description = "What permissions/google grants infraharvest-ci-readonly on the project."
  value       = module.readonly.granted_roles
}

output "gh_commands" {
  description = "Set the variables of the repository's sandbox environment that the sandbox-google workflow reads. Run them where gh is signed in to GitHub."
  # The second project shares the first's GOOGLE_REGION.
  value = var.slot == "" ? join("\n", [
    "gh variable set GOOGLE_PROJECT_ID --env sandbox --repo ${var.github_repository} --body ${var.project_id}",
    "gh variable set GOOGLE_REGION --env sandbox --repo ${var.github_repository} --body ${var.region}",
    "gh variable set GOOGLE_WORKLOAD_IDENTITY_PROVIDER --env sandbox --repo ${var.github_repository} --body ${google_iam_workload_identity_pool_provider.github.name}",
    "gh variable set GOOGLE_FIXTURE_SERVICE_ACCOUNT --env sandbox --repo ${var.github_repository} --body ${google_service_account.fixture.email}",
    "gh variable set GOOGLE_READONLY_SERVICE_ACCOUNT --env sandbox --repo ${var.github_repository} --body ${google_service_account.readonly.email}",
    "",
    ]) : join("\n", [
    "gh variable set GOOGLE_SECOND_PROJECT_ID --env sandbox --repo ${var.github_repository} --body ${var.project_id}",
    "gh variable set GOOGLE_SECOND_WORKLOAD_IDENTITY_PROVIDER --env sandbox --repo ${var.github_repository} --body ${google_iam_workload_identity_pool_provider.github.name}",
    "gh variable set GOOGLE_SECOND_FIXTURE_SERVICE_ACCOUNT --env sandbox --repo ${var.github_repository} --body ${google_service_account.fixture.email}",
    "gh variable set GOOGLE_SECOND_READONLY_SERVICE_ACCOUNT --env sandbox --repo ${var.github_repository} --body ${google_service_account.readonly.email}",
    "",
  ])
}

output "terragram_service_account" {
  description = "The email of terragram-ci-sandbox, which Terragram's sandbox workflow signs in as (GOOGLE_SERVICE_ACCOUNT in its sandbox environment). Empty unless terragram_enabled."
  value       = var.terragram_enabled ? google_service_account.terragram[0].email : ""
}

output "terragram_workload_identity_provider" {
  description = "The full name of the provider Terragram's sandbox workflow signs in through (GOOGLE_WORKLOAD_IDENTITY_PROVIDER in its sandbox environment). Empty unless terragram_enabled."
  value       = var.terragram_enabled ? google_iam_workload_identity_pool_provider.terragram[0].name : ""
}

output "terragram_gh_commands" {
  description = "Set the variables of the Terragram repository's sandbox environment that its sandbox-google workflow reads. Run them where gh is signed in as an administrator of that repository. Empty unless terragram_enabled."
  value = !var.terragram_enabled ? "" : join("\n", [
    "gh variable set GOOGLE_PROJECT_ID --env ${var.terragram_environment} --repo ${var.terragram_repository} --body ${var.project_id}",
    "gh variable set GOOGLE_REGION --env ${var.terragram_environment} --repo ${var.terragram_repository} --body ${var.region}",
    "gh variable set GOOGLE_WORKLOAD_IDENTITY_PROVIDER --env ${var.terragram_environment} --repo ${var.terragram_repository} --body ${google_iam_workload_identity_pool_provider.terragram[0].name}",
    "gh variable set GOOGLE_SERVICE_ACCOUNT --env ${var.terragram_environment} --repo ${var.terragram_repository} --body ${google_service_account.terragram[0].email}",
    "",
  ])
}
