# aws_apigatewayv2_integration, from data/aws/api_gatewayv2.yaml

import {
  to = aws_apigatewayv2_integration.q1w2e3
  id = "h1j2k3l4m5/q1w2e3"
}

# Left out where the plan stays the same, as the default: connection_type=INTERNET, timeout_milliseconds=30000

# Generated configuration, without the arguments the catalogue omits:
resource "aws_apigatewayv2_integration" "q1w2e3" {
  api_id                 = "h1j2k3l4m5"
  connection_type        = "INTERNET"
  integration_method     = "POST"
  integration_type       = "AWS_PROXY"
  integration_uri        = "arn:aws:lambda:eu-west-2:123456789012:function:orders"
  payload_format_version = "2.0"
  timeout_milliseconds   = 30000
}
