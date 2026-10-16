resource "aws_apigatewayv2_integration" "q1w2e3" {
  api_id                 = "h1j2k3l4m5"
  connection_type        = "INTERNET"
  integration_method     = "POST"
  integration_type       = "AWS_PROXY"
  integration_uri        = "arn:aws:lambda:eu-west-2:123456789012:function:orders"
  payload_format_version = "2.0"
  region                 = "eu-west-2"
  timeout_milliseconds   = 30000
}
