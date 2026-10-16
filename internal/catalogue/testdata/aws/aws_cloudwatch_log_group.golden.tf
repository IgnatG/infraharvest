# aws_cloudwatch_log_group, from data/aws/logs.yaml

import {
  to = aws_cloudwatch_log_group.app_web
  id = "/app/web"
}

# /aws/lambda/orders: left out by default, would import as "/aws/lambda/orders": log group Lambda creates for a function

# Left out where the plan stays the same, as the default: deletion_protection_enabled=false, log_group_class=STANDARD

# Generated configuration, without the arguments the catalogue omits:
resource "aws_cloudwatch_log_group" "app_web" {
  deletion_protection_enabled = false
  log_group_class             = "STANDARD"
  name                        = "/app/web"
  retention_in_days           = 30
  skip_destroy                = false
}
