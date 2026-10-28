output "fixture_role_arn" {
  description = "The ARN of infraharvest-ci-fixture: AWS_FIXTURE_ROLE_ARN."
  value       = aws_iam_role.fixture.arn
}

output "readonly_role_arn" {
  description = "The ARN of infraharvest-ci-readonly: AWS_READONLY_ROLE_ARN."
  value       = module.readonly.role_arn
}

output "boundary_policy_arn" {
  description = "The permissions boundary of the estates' roles: AWS_BOUNDARY_POLICY_ARN."
  value       = aws_iam_policy.boundary.arn
}

output "gh_commands" {
  description = "Set the variables of the repository's sandbox environment that the sandbox-aws workflow reads. Run them where gh is signed in to GitHub."
  value       = <<-EOT
    gh variable set AWS_FIXTURE_ROLE_ARN --env sandbox --repo ${var.github_repository} --body ${aws_iam_role.fixture.arn}
    gh variable set AWS_READONLY_ROLE_ARN --env sandbox --repo ${var.github_repository} --body ${module.readonly.role_arn}
    gh variable set AWS_BOUNDARY_POLICY_ARN --env sandbox --repo ${var.github_repository} --body ${aws_iam_policy.boundary.arn}
    gh variable set AWS_REGION --env sandbox --repo ${var.github_repository} --body ${var.region}
  EOT
}
