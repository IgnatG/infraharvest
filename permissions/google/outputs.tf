output "granted_roles" {
  description = "Roles granted to the member on the project."
  value       = sort([for binding in google_project_iam_member.readonly : binding.role])
}

output "denied_permissions" {
  description = "Data reads the deny policy denies the member on the project; empty when deny_data_reads is false."
  value       = var.deny_data_reads ? local.denied_data_reads : []
}
