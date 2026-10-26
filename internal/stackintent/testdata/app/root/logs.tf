resource "aws_cloudwatch_log_group" "app" {
  name              = "/app/prod"
  retention_in_days = 30
}
