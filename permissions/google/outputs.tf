output "granted_roles" {
  description = "Roles granted to the member on the project."
  value       = sort([for binding in google_project_iam_member.readonly : binding.role])
}
