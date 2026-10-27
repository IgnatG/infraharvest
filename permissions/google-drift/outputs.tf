output "granted_roles" {
  description = "Roles granted to the member: on the project, and on the state's bucket, or the objects under its prefix."
  value       = concat(sort([for binding in google_project_iam_member.readonly : binding.role]), ["${google_storage_bucket_iam_member.state.role} on ${local.state_objects}"])
}

output "denied_permissions" {
  description = "Data reads the deny policy denies the member on the project; empty when deny_data_reads is false."
  value       = var.deny_data_reads ? local.denied_data_reads : []
}
