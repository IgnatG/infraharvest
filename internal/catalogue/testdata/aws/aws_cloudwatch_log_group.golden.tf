# aws_cloudwatch_log_group, from data/aws/logs.yaml

import {
  to = aws_cloudwatch_log_group.app_web
  id = "/app/web"
}

import {
  to = aws_cloudwatch_log_group.lambda_invoices
  id = "/aws/lambda/invoices"
}

import {
  to = aws_cloudwatch_log_group.lambda_payments
  id = "/aws/lambda/payments"
}

# /aws/lambda/orders: left out by default, would import as "/aws/lambda/orders": a function's log group as Lambda creates it when the function first logs, without retention or encryption; select it to import it

# Left out where the plan stays the same, as the default: deletion_protection_enabled=false, log_group_class=STANDARD

# Generated configuration, without the arguments the catalogue omits:
resource "aws_cloudwatch_log_group" "app_web" {
  deletion_protection_enabled = false
  log_group_class             = "STANDARD"
  name                        = "/app/web"
  retention_in_days           = 30
  skip_destroy                = false
}
