resource "aws_apigatewayv2_stage" "orders_default" {
  api_id        = "h1j2k3l4m5"
  auto_deploy   = true
  deployment_id = "d9f8e7"
  name          = "$default"
  region        = "eu-west-2"
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
  region        = "eu-west-2"
}
