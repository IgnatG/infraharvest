# aws_cloudwatch_log_group, from data/aws/logs.yaml

import {
  to = aws_cloudwatch_log_group.app_web
  id = "/app/web"
}

# /aws/lambda/orders: left out by default, would import as "/aws/lambda/orders": log group Lambda creates for a function
