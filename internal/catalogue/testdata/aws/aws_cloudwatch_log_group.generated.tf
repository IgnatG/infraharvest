resource "aws_cloudwatch_log_group" "app_web" {
  deletion_protection_enabled = false
  log_group_class             = "STANDARD"
  name                        = "/app/web"
  region                      = "eu-west-2"
  retention_in_days           = 30
  skip_destroy                = false
}
