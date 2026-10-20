output "role_arn" {
  description = "ARN of the drift role, for --profile or role_arn settings."
  value       = aws_iam_role.drift.arn
}
