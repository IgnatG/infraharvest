# aws_apigatewayv2_stage, from data/aws/api_gatewayv2.yaml

import {
  to = aws_apigatewayv2_stage.orders_default
  id = "h1j2k3l4m5/$default"
}

import {
  to = aws_apigatewayv2_stage.orders_v1
  id = "h1j2k3l4m5/v1"
}

# Left out of generated configuration: region, tags_all, deployment_id when auto_deploy=true

# Left out where the plan stays the same, as the default: default_route_settings.logging_level=OFF

# Generated configuration, without the arguments the catalogue omits:
resource "aws_apigatewayv2_stage" "orders_default" {
  api_id      = "h1j2k3l4m5"
  auto_deploy = true
  name        = "$default"
  default_route_settings {
    data_trace_enabled       = false
    detailed_metrics_enabled = false
    logging_level            = "OFF"
    throttling_burst_limit   = 0
    throttling_rate_limit    = 0
  }
}

resource "aws_apigatewayv2_stage" "orders_v1" {
  api_id        = "h1j2k3l4m5"
  auto_deploy   = false
  deployment_id = "c6b5a4"
  name          = "v1"
}
