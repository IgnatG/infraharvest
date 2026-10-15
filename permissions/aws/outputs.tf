output "role_arn" {
  description = "ARN of the read-only role, for --profile or role_arn settings."
  value       = aws_iam_role.readonly.arn
}
