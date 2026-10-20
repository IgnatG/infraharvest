output "granted_roles" {
  description = "Roles granted to the member: on the project, and on the state's bucket."
  value       = concat(sort([for binding in google_project_iam_member.readonly : binding.role]), ["${google_storage_bucket_iam_member.state.role} on gs://${var.state_bucket}"])
}
