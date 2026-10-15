# aws_lb_target_group, from data/aws/alb.yaml

import {
  to = aws_lb_target_group.web
  id = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web/0123456789abcdef"
}

# Kept in state only, so a plan may set: lambda_multi_value_headers_enabled, proxy_protocol_v2
