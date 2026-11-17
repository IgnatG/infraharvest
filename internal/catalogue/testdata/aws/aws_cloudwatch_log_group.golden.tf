# aws_cloudwatch_log_group, from data/aws/logs.yaml

import {
  to = aws_cloudwatch_log_group.app_web
  id = "/app/web"
}

import {
  to = aws_cloudwatch_log_group.eks_staging_cluster
  id = "/aws/eks/staging/cluster"
}

import {
  to = aws_cloudwatch_log_group.lambda_invoices
  id = "/aws/lambda/invoices"
}

import {
  to = aws_cloudwatch_log_group.lambda_payments
  id = "/aws/lambda/payments"
}

import {
  to = aws_cloudwatch_log_group.rds_billing_error
  id = "/aws/rds/instance/billing/error"
}

# /aws/lambda/orders: left out by default, would import as "/aws/lambda/orders": a function's log group as Lambda creates it when the function first logs, without retention or encryption; select it to import it
# API-Gateway-Execution-Logs_a1b2c3d4e5/prod: left out by default, would import as "API-Gateway-Execution-Logs_a1b2c3d4e5/prod": a stage's execution log group as API Gateway creates it when execution logging is turned on, without retention or encryption; select it to import it
# /aws/rds/instance/orders/error: left out by default, would import as "/aws/rds/instance/orders/error": a database's log export group as RDS creates it when a log type is exported, without retention or encryption; select it to import it
# /aws/eks/main/cluster: left out by default, would import as "/aws/eks/main/cluster": a cluster's control plane log group as EKS creates it when control plane logging is turned on, without retention or encryption; select it to import it

# Left out where the plan stays the same, as the default: deletion_protection_enabled=false, log_group_class=STANDARD

# Generated configuration, without the arguments the catalogue omits:
resource "aws_cloudwatch_log_group" "app_web" {
  deletion_protection_enabled = false
  log_group_class             = "STANDARD"
  name                        = "/app/web"
  retention_in_days           = 30
  skip_destroy                = false
}
